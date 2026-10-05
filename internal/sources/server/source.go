// Package server exposes metrics for Hetzner Cloud servers.
package server

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcmetrics"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/sources/pricing"
)

var descs = hcmetrics.NewDescSet("server", "server")

var (
	infoDesc            = descs.New("info", "Server information, always 1", "server_id", "status", "server_type", "location")
	runningDesc         = descs.New("running", "Whether the server is running (1=running, 0=any other status)")
	outgoingTrafficDesc = descs.New("outgoing_traffic_bytes", "Outbound traffic in the current billing period (bytes)")
	ingoingTrafficDesc  = descs.New("ingoing_traffic_bytes", "Inbound traffic in the current billing period (bytes)")
	includedTrafficDesc = descs.New("included_traffic_bytes", "Free outbound traffic included in the current billing period (bytes)")
	cpuUsageDesc        = descs.New("cpu_usage_ratio", "CPU usage across all vCPUs (0-1, 1 = all vCPUs busy)")

	// From the server and its server type, no extra API requests.
	cpuCoresDesc          = descs.New("cpu_cores", "Number of vCPUs of the server type")
	memoryDesc            = descs.New("memory_bytes", "Memory of the server type (bytes)")
	diskDesc              = descs.New("disk_bytes", "Size of the primary disk (bytes)")
	lockedDesc            = descs.New("locked", "Whether the server is locked by Hetzner, e.g. during a running action (1=locked)")
	rescueDesc            = descs.New("rescue_enabled", "Whether the rescue system is enabled for the next boot (1=enabled)")
	backupDesc            = descs.New("backup_enabled", "Whether backups are enabled (1=enabled)")
	deleteProtectionDesc  = descs.New("delete_protection", "Whether delete protection is enabled (1=enabled)")
	rebuildProtectionDesc = descs.New("rebuild_protection", "Whether rebuild protection is enabled (1=enabled)")
	typeDeprecatedDesc    = descs.New("type_deprecated", "Whether the server type is deprecated in the server's location (1=deprecated)")
	typeUnavailableDesc   = descs.New("type_unavailable_after_timestamp_seconds",
		"Unix timestamp after which the deprecated server type can no longer be ordered in the server's location")
	createdDesc    = descs.New("created_timestamp_seconds", "Unix timestamp when the server was created")
	lastBackupDesc = descs.New("last_backup_timestamp_seconds",
		"Unix timestamp when the server's newest available backup was created (from the images collector)")

	// Prices of the server type in the server's location, net (without VAT).
	monthlyPriceDesc       = descs.New("monthly_price", "Net monthly price of the server (capped monthly price, without backups)", "currency")
	hourlyPriceDesc        = descs.New("hourly_price", "Net hourly price of the server", "currency")
	backupMonthlyPriceDesc = descs.New("backup_monthly_price", "Net monthly price of the server's backups (only if enabled)", "currency")
	trafficPriceDesc       = descs.New("traffic_price_per_tb", "Net price per TB of outbound traffic above the included traffic", "currency")
)

// seriesDescs maps Hetzner time series keys ("<category>.<id>.<metric>") to
// metrics. The <id> part (disk or interface number) becomes the extra label.
var seriesDescs = map[seriesKey]*prometheus.Desc{
	{"disk", "bandwidth.read"}:   descs.New("disk_read_bytes_per_second", "Disk read (bytes/s)", "disk"),
	{"disk", "bandwidth.write"}:  descs.New("disk_write_bytes_per_second", "Disk write (bytes/s)", "disk"),
	{"disk", "iops.read"}:        descs.New("disk_read_iops", "Disk read operations per second", "disk"),
	{"disk", "iops.write"}:       descs.New("disk_write_iops", "Disk write operations per second", "disk"),
	{"network", "bandwidth.in"}:  descs.New("network_in_bytes_per_second", "Public network inbound traffic (bytes/s)", "interface"),
	{"network", "bandwidth.out"}: descs.New("network_out_bytes_per_second", "Public network outbound traffic (bytes/s)", "interface"),
	{"network", "pps.in"}:        descs.New("network_in_packets_per_second", "Public network inbound packets per second", "interface"),
	{"network", "pps.out"}:       descs.New("network_out_packets_per_second", "Public network outbound packets per second", "interface"),
}

type seriesKey struct {
	category string
	metric   string
}

type serverData struct {
	server  *hcloud.Server
	metrics *hcloud.ServerMetrics // nil if the server is not running or the fetch failed
}

// Source exposes metrics for Hetzner Cloud servers.
type Source struct {
	client      *hcloud.Client
	concurrency int
	prices      pricing.Lookup             // for the backup surcharge, may be nil
	lastBackups func() map[int64]time.Time // newest backup per server ID, may be nil
	schedule    *hcmetrics.MetricsSchedule[*hcloud.ServerMetrics]

	mu      sync.RWMutex
	servers []serverData
}

// New returns the servers source. metricsInterval is how often CPU, disk and
// network metrics (one request per running server) are fetched; 0 turns them
// off. prices may be nil (no backup price then). lastBackups returns the
// newest backup per server ID (from the images source) and may be nil.
func New(client *hcloud.Client, concurrency int, metricsInterval time.Duration, prices pricing.Lookup,
	lastBackups func() map[int64]time.Time) *Source {
	return &Source{
		client:      client,
		concurrency: concurrency,
		prices:      prices,
		lastBackups: lastBackups,
		schedule:    hcmetrics.NewMetricsSchedule[*hcloud.ServerMetrics](metricsInterval),
	}
}

func (s *Source) Name() string { return "servers" }

func (s *Source) Fetch(ctx context.Context) error {
	listCtx, cancel := context.WithTimeout(ctx, hcmetrics.ListTimeout)
	defer cancel()

	servers, err := s.client.Server.All(listCtx)
	if err != nil {
		// Keep the previous data so a transient error doesn't create gaps.
		return fmt.Errorf("listing servers: %w", err)
	}

	// Metrics cost one request per running server: all of them every
	// metrics interval; new ones, failed ones (one retry) and servers whose
	// status changed (e.g. started since) on the next poll.
	metrics, metricsErr := hcmetrics.FetchMetrics(ctx, time.Now(), s.schedule, servers,
		func(server *hcloud.Server) int64 { return server.ID },
		func(server *hcloud.Server) string { return string(server.Status) },
		s.concurrency, s.fetchMetrics)
	if ctx.Err() != nil {
		return ctx.Err() // shutting down, keep the previous data
	}

	data := make([]serverData, len(servers))
	for i, server := range servers {
		data[i] = serverData{server: server}
		// A server stopped since the last fetch must not show frozen values.
		if server.Status == hcloud.ServerStatusRunning {
			data[i].metrics = metrics[i]
		}
	}

	s.mu.Lock()
	s.servers = data
	s.mu.Unlock()

	if metricsErr != nil {
		return fmt.Errorf("fetching server metrics: %w", metricsErr)
	}
	return nil
}

func (s *Source) fetchMetrics(ctx context.Context, server *hcloud.Server) (*hcloud.ServerMetrics, error) {
	if server.Status != hcloud.ServerStatusRunning {
		return nil, nil // stopped servers have no metrics, save the API request
	}

	start, end, step := hcmetrics.MetricsWindow()
	metrics, _, err := s.client.Server.GetMetrics(ctx, server, hcloud.ServerGetMetricsOpts{
		Start: start,
		End:   end,
		Step:  step,
		Types: []hcloud.ServerMetricType{
			hcloud.ServerMetricCPU,
			hcloud.ServerMetricDisk,
			hcloud.ServerMetricNetwork,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("server %s: %w", server.Name, err)
	}
	return metrics, nil
}

// NameByID returns the name of the server with the given ID from the last
// poll, or "" if unknown. Other sources use it to label servers by name.
func (s *Source) NameByID(id int64) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, data := range s.servers {
		if data.server.ID == id {
			return data.server.Name
		}
	}
	return ""
}

func (s *Source) Describe(ch chan<- *prometheus.Desc) {
	descs.Describe(ch)
}

func (s *Source) Collect(ch chan<- prometheus.Metric) {
	s.mu.RLock()
	servers := s.servers
	s.mu.RUnlock()

	var lastBackups map[int64]time.Time
	if s.lastBackups != nil {
		lastBackups = s.lastBackups()
	}

	for _, data := range servers {
		collectServer(ch, data.server)
		if ts, ok := hcmetrics.Timestamp(lastBackups[data.server.ID]); ok {
			ch <- prometheus.MustNewConstMetric(lastBackupDesc, prometheus.GaugeValue, ts, data.server.Name)
		}
		collectPrices(ch, data.server, s.prices)
		if data.metrics != nil {
			collectMetrics(ch, data.server, data.metrics)
		}
	}
}

func collectServer(ch chan<- prometheus.Metric, server *hcloud.Server) {
	var serverType, location string
	if server.ServerType != nil {
		serverType = server.ServerType.Name
	}
	if server.Location != nil {
		location = server.Location.Name
	}

	ch <- prometheus.MustNewConstMetric(infoDesc, prometheus.GaugeValue, 1,
		server.Name, strconv.FormatInt(server.ID, 10), string(server.Status), serverType, location)
	ch <- prometheus.MustNewConstMetric(runningDesc, prometheus.GaugeValue,
		hcmetrics.BoolToFloat(server.Status == hcloud.ServerStatusRunning), server.Name)
	ch <- prometheus.MustNewConstMetric(outgoingTrafficDesc, prometheus.GaugeValue, float64(server.OutgoingTraffic), server.Name)
	ch <- prometheus.MustNewConstMetric(ingoingTrafficDesc, prometheus.GaugeValue, float64(server.IngoingTraffic), server.Name)
	ch <- prometheus.MustNewConstMetric(includedTrafficDesc, prometheus.GaugeValue, float64(server.IncludedTraffic), server.Name)

	gauge := func(desc *prometheus.Desc, value float64) {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, value, server.Name)
	}
	gauge(diskDesc, float64(server.PrimaryDiskSize)*hcmetrics.Gibibyte) // the API reports GB
	gauge(lockedDesc, hcmetrics.BoolToFloat(server.Locked))
	gauge(rescueDesc, hcmetrics.BoolToFloat(server.RescueEnabled))
	gauge(backupDesc, hcmetrics.BoolToFloat(server.BackupWindow != ""))
	gauge(deleteProtectionDesc, hcmetrics.BoolToFloat(server.Protection.Delete))
	gauge(rebuildProtectionDesc, hcmetrics.BoolToFloat(server.Protection.Rebuild))
	if ts, ok := hcmetrics.Timestamp(server.Created); ok {
		gauge(createdDesc, ts)
	}

	if st := server.ServerType; st != nil {
		gauge(cpuCoresDesc, float64(st.Cores))
		gauge(memoryDesc, float64(st.Memory)*hcmetrics.Gibibyte) // the API reports GB

		deprecation := typeDeprecation(st, location)
		gauge(typeDeprecatedDesc, hcmetrics.BoolToFloat(deprecation != nil))
		if deprecation != nil && !deprecation.UnavailableAfter.IsZero() {
			gauge(typeUnavailableDesc, float64(deprecation.UnavailableAfter.Unix()))
		}
	}
}

func collectPrices(ch chan<- prometheus.Metric, server *hcloud.Server, prices pricing.Lookup) {
	currency, known := prices.Currency()
	if server.ServerType == nil || server.Location == nil || !known {
		return
	}
	for _, p := range server.ServerType.Pricings {
		if p.Location == nil || p.Location.Name != server.Location.Name {
			continue
		}
		price := func(desc *prometheus.Desc, value string) (float64, bool) {
			v, ok := hcmetrics.ParsePrice(value)
			if ok {
				ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, v, server.Name, currency)
			}
			return v, ok
		}
		monthly, ok := price(monthlyPriceDesc, p.Monthly.Net)
		price(hourlyPriceDesc, p.Hourly.Net)
		price(trafficPriceDesc, p.PerTBTraffic.Net)

		if percent, havePercent := prices.BackupPercent(); ok && havePercent && server.BackupWindow != "" {
			ch <- prometheus.MustNewConstMetric(backupMonthlyPriceDesc, prometheus.GaugeValue,
				monthly*percent/100, server.Name, currency)
		}
		return
	}
}

// typeDeprecation returns the deprecation of the server type in the given
// location, or nil. Hetzner deprecates server types per location.
func typeDeprecation(st *hcloud.ServerType, location string) *hcloud.DeprecationInfo {
	for _, l := range st.Locations {
		if l.Location != nil && l.Location.Name == location {
			return l.Deprecation
		}
	}
	return nil
}

func metricValue(v hcloud.ServerMetricsValue) string { return v.Value }

func collectMetrics(ch chan<- prometheus.Metric, server *hcloud.Server, metrics *hcloud.ServerMetrics) {
	serverName := server.Name
	for key, series := range metrics.TimeSeries {
		val, ok := hcmetrics.LastValue(series, metricValue)
		if !ok {
			continue
		}

		if key == string(hcloud.ServerMetricCPU) {
			// Hetzner reports percent with 100 per vCPU; normalise to 0-1 of all vCPUs.
			if server.ServerType != nil && server.ServerType.Cores > 0 {
				ratio := val / 100 / float64(server.ServerType.Cores)
				ch <- prometheus.MustNewConstMetric(cpuUsageDesc, prometheus.GaugeValue, ratio, serverName)
			}
			continue
		}

		// e.g. "disk.0.bandwidth.read" -> category "disk", id "0", metric "bandwidth.read"
		parts := strings.SplitN(key, ".", 3)
		if len(parts) != 3 {
			log.WithFields(log.Fields{"key": key, "server": serverName}).Debug("Unexpected metric key format")
			continue
		}
		if desc, ok := seriesDescs[seriesKey{category: parts[0], metric: parts[2]}]; ok {
			ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, val, serverName, parts[1])
		}
	}
}
