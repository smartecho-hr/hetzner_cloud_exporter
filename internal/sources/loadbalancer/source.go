// Package loadbalancer exposes metrics for Hetzner Cloud Load Balancers and their targets.
package loadbalancer

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcmetrics"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/sources/pricing"
)

var descs = hcmetrics.NewDescSet("load_balancer", "load_balancer")

var (
	infoDesc              = descs.New("info", "Load Balancer information, always 1", "load_balancer_id", "load_balancer_type", "location")
	outgoingTrafficDesc   = descs.New("outgoing_traffic_bytes", "Outbound traffic in the current billing period (bytes)")
	ingoingTrafficDesc    = descs.New("ingoing_traffic_bytes", "Inbound traffic in the current billing period (bytes)")
	includedTrafficDesc   = descs.New("included_traffic_bytes", "Free outbound traffic included in the current billing period (bytes)")
	openConnectionsDesc   = descs.New("open_connections", "Current open connections on the Load Balancer")
	connectionsPerSecDesc = descs.New("connections_per_second", "Connections per second on the Load Balancer")
	requestsPerSecDesc    = descs.New("requests_per_second", "HTTP requests per second on the Load Balancer (0 for TCP-only Load Balancers)")
	bandwidthDesc         = descs.New("bandwidth_bytes_per_second", "Bandwidth of the Load Balancer (bytes/s)", "direction")

	// Limits of the Load Balancer type, as reported by the API, and current usage.
	maxConnectionsDesc  = descs.New("max_connections", "Maximum concurrent connections of the Load Balancer type")
	maxServicesDesc     = descs.New("max_services", "Maximum number of services of the Load Balancer type")
	maxTargetsDesc      = descs.New("max_targets", "Maximum number of targets of the Load Balancer type")
	maxCertificatesDesc = descs.New("max_certificates", "Maximum number of assigned certificates of the Load Balancer type")
	servicesDesc        = descs.New("services", "Number of configured services")
	targetsDesc         = descs.New("targets", "Number of targets, with label selectors expanded into the servers they match")
	certificatesDesc    = descs.New("certificates", "Number of distinct certificates assigned to services")

	deleteProtectionDesc = descs.New("delete_protection", "Whether delete protection is enabled (1=enabled)")
	typeDeprecatedDesc   = descs.New("type_deprecated", "Whether the Load Balancer type is deprecated (1=deprecated)")
	typeUnavailableDesc  = descs.New("type_unavailable_after_timestamp_seconds",
		"Unix timestamp after which the deprecated Load Balancer type can no longer be ordered")

	// Services, one series per listen port.
	serviceInfoDesc = descs.New("service_info", "Load Balancer service configuration, always 1",
		"listen_port", "protocol", "destination_port", "proxyprotocol")
	healthCheckIntervalDesc = descs.New("health_check_interval_seconds", "Interval of the service's health check",
		"listen_port")
	healthCheckTimeoutDesc = descs.New("health_check_timeout_seconds", "Timeout of the service's health check",
		"listen_port")
	healthCheckRetriesDesc = descs.New("health_check_retries", "Failed health checks before a target is marked unhealthy",
		"listen_port")

	createdDesc      = descs.New("created_timestamp_seconds", "Unix timestamp when the Load Balancer was created")
	monthlyPriceDesc = descs.New("monthly_price", "Net monthly price of the Load Balancer", "currency")
	hourlyPriceDesc  = descs.New("hourly_price", "Net hourly price of the Load Balancer", "currency")
	trafficPriceDesc = descs.New("traffic_price_per_tb", "Net price per TB of outbound traffic above the included traffic", "currency")
)

type loadBalancerData struct {
	loadBalancer *hcloud.LoadBalancer
	targets      []target
	metrics      *hcloud.LoadBalancerMetrics // nil if the fetch failed
}

// Source exposes metrics for Hetzner Cloud Load Balancers and their targets.
type Source struct {
	client      *hcloud.Client
	concurrency int
	serverName  func(id int64) string // resolves target server names, may be nil
	prices      pricing.Lookup        // for the account's currency, may be nil
	schedule    *hcmetrics.MetricsSchedule[*hcloud.LoadBalancerMetrics]

	mu            sync.RWMutex
	loadBalancers []loadBalancerData
}

// New returns the Load Balancer source. metricsInterval is how often
// connection, request and bandwidth metrics (one request per Load Balancer)
// are fetched; 0 turns them off. serverName resolves a server ID to its name
// for the target labels; prices gives the account's currency. Both may be nil.
func New(client *hcloud.Client, concurrency int, metricsInterval time.Duration, serverName func(id int64) string, prices pricing.Lookup) *Source {
	return &Source{
		client:      client,
		concurrency: concurrency,
		serverName:  serverName,
		prices:      prices,
		schedule:    hcmetrics.NewMetricsSchedule[*hcloud.LoadBalancerMetrics](metricsInterval),
	}
}

func (s *Source) Name() string { return "load_balancers" }

func (s *Source) Fetch(ctx context.Context) error {
	listCtx, cancel := context.WithTimeout(ctx, hcmetrics.ListTimeout)
	defer cancel()

	listed, err := listLoadBalancers(listCtx, s.client)
	if err != nil {
		// Keep the previous data so a transient error doesn't create gaps.
		return fmt.Errorf("listing load balancers: %w", err)
	}

	// Metrics cost one request per Load Balancer: all of them every metrics
	// interval; new ones and failed ones (one retry) on the next poll.
	metrics, metricsErr := hcmetrics.FetchMetrics(ctx, time.Now(), s.schedule, listed,
		func(l listedLoadBalancer) int64 { return l.loadBalancer.ID }, nil, s.concurrency, s.fetchMetrics)
	if ctx.Err() != nil {
		return ctx.Err() // shutting down, keep the previous data
	}

	data := make([]loadBalancerData, len(listed))
	for i, l := range listed {
		data[i] = loadBalancerData{
			loadBalancer: l.loadBalancer,
			targets:      resolveTargets(l.loadBalancer.Name, l.targets),
			metrics:      metrics[i],
		}
	}

	s.mu.Lock()
	s.loadBalancers = data
	s.mu.Unlock()

	if metricsErr != nil {
		return fmt.Errorf("fetching load balancer metrics: %w", metricsErr)
	}
	return nil
}

func (s *Source) fetchMetrics(ctx context.Context, l listedLoadBalancer) (*hcloud.LoadBalancerMetrics, error) {
	start, end, step := hcmetrics.MetricsWindow()
	metrics, _, err := s.client.LoadBalancer.GetMetrics(ctx, l.loadBalancer, hcloud.LoadBalancerGetMetricsOpts{
		Start: start,
		End:   end,
		Step:  step,
		Types: []hcloud.LoadBalancerMetricType{
			hcloud.LoadBalancerMetricOpenConnections,
			hcloud.LoadBalancerMetricConnectionsPerSecond,
			hcloud.LoadBalancerMetricRequestsPerSecond,
			hcloud.LoadBalancerMetricBandwidth,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("load balancer %s: %w", l.loadBalancer.Name, err)
	}
	return metrics, nil
}

func (s *Source) Describe(ch chan<- *prometheus.Desc) {
	descs.Describe(ch)
}

func (s *Source) Collect(ch chan<- prometheus.Metric) {
	s.mu.RLock()
	loadBalancers := s.loadBalancers
	s.mu.RUnlock()

	for _, data := range loadBalancers {
		lb := data.loadBalancer
		collectLoadBalancer(ch, lb)
		collectLimits(ch, lb, len(data.targets))
		collectServices(ch, lb)
		collectPrices(ch, lb, s.prices)
		collectTargets(ch, lb.Name, data.targets, s.serverName)
		if data.metrics != nil {
			collectMetrics(ch, lb, data.metrics)
		}
	}
}

func collectLoadBalancer(ch chan<- prometheus.Metric, lb *hcloud.LoadBalancer) {
	var lbType, location string
	if lb.LoadBalancerType != nil {
		lbType = lb.LoadBalancerType.Name
	}
	if lb.Location != nil {
		location = lb.Location.Name
	}

	ch <- prometheus.MustNewConstMetric(infoDesc, prometheus.GaugeValue, 1,
		lb.Name, strconv.FormatInt(lb.ID, 10), lbType, location)
	ch <- prometheus.MustNewConstMetric(outgoingTrafficDesc, prometheus.GaugeValue, float64(lb.OutgoingTraffic), lb.Name)
	ch <- prometheus.MustNewConstMetric(ingoingTrafficDesc, prometheus.GaugeValue, float64(lb.IngoingTraffic), lb.Name)
	ch <- prometheus.MustNewConstMetric(includedTrafficDesc, prometheus.GaugeValue, float64(lb.IncludedTraffic), lb.Name)
}

func collectLimits(ch chan<- prometheus.Metric, lb *hcloud.LoadBalancer, targetCount int) {
	if lbType := lb.LoadBalancerType; lbType != nil {
		ch <- prometheus.MustNewConstMetric(maxConnectionsDesc, prometheus.GaugeValue, float64(lbType.MaxConnections), lb.Name)
		ch <- prometheus.MustNewConstMetric(maxServicesDesc, prometheus.GaugeValue, float64(lbType.MaxServices), lb.Name)
		ch <- prometheus.MustNewConstMetric(maxTargetsDesc, prometheus.GaugeValue, float64(lbType.MaxTargets), lb.Name)
		ch <- prometheus.MustNewConstMetric(maxCertificatesDesc, prometheus.GaugeValue, float64(lbType.MaxAssignedCertificates), lb.Name)
	}

	certificates := make(map[int64]bool)
	for _, service := range lb.Services {
		for _, cert := range service.HTTP.Certificates {
			if cert != nil {
				certificates[cert.ID] = true
			}
		}
	}

	ch <- prometheus.MustNewConstMetric(servicesDesc, prometheus.GaugeValue, float64(len(lb.Services)), lb.Name)
	ch <- prometheus.MustNewConstMetric(targetsDesc, prometheus.GaugeValue, float64(targetCount), lb.Name)
	ch <- prometheus.MustNewConstMetric(certificatesDesc, prometheus.GaugeValue, float64(len(certificates)), lb.Name)
}

func collectServices(ch chan<- prometheus.Metric, lb *hcloud.LoadBalancer) {
	ch <- prometheus.MustNewConstMetric(deleteProtectionDesc, prometheus.GaugeValue,
		hcmetrics.BoolToFloat(lb.Protection.Delete), lb.Name)
	if t := lb.LoadBalancerType; t != nil {
		ch <- prometheus.MustNewConstMetric(typeDeprecatedDesc, prometheus.GaugeValue,
			hcmetrics.BoolToFloat(t.IsDeprecated()), lb.Name)
		if t.IsDeprecated() {
			if ts, ok := hcmetrics.Timestamp(t.UnavailableAfter()); ok {
				ch <- prometheus.MustNewConstMetric(typeUnavailableDesc, prometheus.GaugeValue, ts, lb.Name)
			}
		}
	}

	for _, service := range lb.Services {
		port := strconv.Itoa(service.ListenPort)
		hc := service.HealthCheck

		ch <- prometheus.MustNewConstMetric(serviceInfoDesc, prometheus.GaugeValue, 1, lb.Name,
			port, string(service.Protocol), strconv.Itoa(service.DestinationPort), strconv.FormatBool(service.Proxyprotocol))
		ch <- prometheus.MustNewConstMetric(healthCheckIntervalDesc, prometheus.GaugeValue, hc.Interval.Seconds(), lb.Name, port)
		ch <- prometheus.MustNewConstMetric(healthCheckTimeoutDesc, prometheus.GaugeValue, hc.Timeout.Seconds(), lb.Name, port)
		ch <- prometheus.MustNewConstMetric(healthCheckRetriesDesc, prometheus.GaugeValue, float64(hc.Retries), lb.Name, port)
	}
}

func collectPrices(ch chan<- prometheus.Metric, lb *hcloud.LoadBalancer, prices pricing.Lookup) {
	if ts, ok := hcmetrics.Timestamp(lb.Created); ok {
		ch <- prometheus.MustNewConstMetric(createdDesc, prometheus.GaugeValue, ts, lb.Name)
	}
	currency, known := prices.Currency()
	if lb.LoadBalancerType == nil || lb.Location == nil || !known {
		return
	}
	for _, p := range lb.LoadBalancerType.Pricings {
		if p.Location == nil || p.Location.Name != lb.Location.Name {
			continue
		}
		for desc, value := range map[*prometheus.Desc]string{
			monthlyPriceDesc: p.Monthly.Net,
			hourlyPriceDesc:  p.Hourly.Net,
			trafficPriceDesc: p.PerTBTraffic.Net,
		} {
			if v, ok := hcmetrics.ParsePrice(value); ok {
				ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, v, lb.Name, currency)
			}
		}
		return
	}
}

func metricValue(v hcloud.LoadBalancerMetricsValue) string { return v.Value }

func collectMetrics(ch chan<- prometheus.Metric, lb *hcloud.LoadBalancer, metrics *hcloud.LoadBalancerMetrics) {
	push := func(desc *prometheus.Desc, key string, labels ...string) bool {
		val, ok := hcmetrics.LastValue(metrics.TimeSeries[key], metricValue)
		if ok {
			ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, val, append([]string{lb.Name}, labels...)...)
		}
		return ok
	}

	push(openConnectionsDesc, string(hcloud.LoadBalancerMetricOpenConnections))
	push(connectionsPerSecDesc, string(hcloud.LoadBalancerMetricConnectionsPerSecond))
	push(bandwidthDesc, "bandwidth.in", "in")
	push(bandwidthDesc, "bandwidth.out", "out")

	// TCP-only Load Balancers have no requests: report 0 so dashboards don't
	// show "no data". For HTTP Load Balancers a missing value stays a gap.
	if !push(requestsPerSecDesc, string(hcloud.LoadBalancerMetricRequestsPerSecond)) && tcpOnly(lb) {
		ch <- prometheus.MustNewConstMetric(requestsPerSecDesc, prometheus.GaugeValue, 0, lb.Name)
	}
}

func tcpOnly(lb *hcloud.LoadBalancer) bool {
	for _, service := range lb.Services {
		if service.Protocol != hcloud.LoadBalancerServiceProtocolTCP {
			return false
		}
	}
	return true
}
