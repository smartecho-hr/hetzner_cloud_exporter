// Package sources lists every Hetzner resource the exporter polls.
package sources

import (
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcmetrics"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/poller"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/sources/certificate"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/sources/floatingip"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/sources/image"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/sources/loadbalancer"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/sources/pricing"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/sources/primaryip"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/sources/server"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/sources/sshkey"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/sources/storagebox"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/sources/volume"
)

// Collector is a source that can be turned on or off with
// --collector.<name> (dashes instead of underscores), COLLECTOR_<NAME> or
// collectors.<name> in the config file.
type Collector struct {
	Name    string // same as the source's Name()
	Default bool
	Help    string
	// Interval is how often the source is fetched by default, changed with
	// --collector.<name>.interval; 0 = every poll (--hcloud.poll-interval).
	Interval time.Duration
}

// Collectors lists every source that can be turned on or off, in poll order.
var Collectors = []Collector{
	{Name: "pricing", Default: true, Help: "prices, used for the *_monthly_price metrics of other collectors", Interval: time.Hour},
	{Name: "servers", Default: true, Help: "servers: status, size, prices, traffic, CPU/disk/network metrics"},
	{Name: "load_balancers", Default: true, Help: "Load Balancers: metrics, limits, services, target health, prices"},
	{Name: "certificates", Default: true, Help: "TLS certificates: expiry and issuance/renewal status"},
	{Name: "volumes", Default: true, Help: "volumes: size, attachment, prices"},
	{Name: "floating_ips", Default: true, Help: "floating IPs: assignment, prices"},
	{Name: "primary_ips", Default: true, Help: "primary IPs: assignment, prices"},
	{Name: "images", Default: true, Help: "snapshots and backups: size, age, prices", Interval: 10 * time.Minute},
	{Name: "storage_boxes", Default: true, Help: "Storage Boxes: quota, usage, access, prices", Interval: 5 * time.Minute},
	{Name: "ssh_keys", Default: true, Help: "SSH keys: inventory", Interval: time.Hour},
}

// Config configures the sources.
type Config struct {
	// Concurrency limits parallel per-resource requests.
	Concurrency int
	// MetricsInterval is how often per-resource metrics (server CPU/disk/
	// network, Load Balancer connections/requests/bandwidth; one request per
	// resource) are fetched. 0 turns them off.
	MetricsInterval time.Duration
	// Enabled maps a collector name to whether it is on; names missing from
	// the map use the collector's default.
	Enabled map[string]bool
	// Intervals maps a collector name to how often it is fetched (0 = every
	// poll); names missing from the map use the collector's Interval.
	Intervals map[string]time.Duration
}

// every makes the poller fetch a source at most once per interval
// (poller.Throttled); 0 means every poll.
type every struct {
	poller.Source
	interval time.Duration
}

func (e every) MinInterval() time.Duration { return e.interval }

// Sources returns the enabled sources.
// To export a new kind of resource, implement poller.Source, add it here and
// to Collectors.
func Sources(client *hcloud.Client, cfg Config) []poller.Source {
	concurrency := cfg.Concurrency
	collector := func(name string) Collector {
		for _, c := range Collectors {
			if c.Name == name {
				return c
			}
		}
		return Collector{Name: name}
	}
	on := func(name string) bool {
		if value, ok := cfg.Enabled[name]; ok {
			return value
		}
		return collector(name).Default
	}

	var result []poller.Source
	add := func(source poller.Source) {
		interval := collector(source.Name()).Interval
		if value, ok := cfg.Intervals[source.Name()]; ok {
			interval = value
		}
		result = append(result, every{Source: source, interval: interval})
	}

	// Prices are looked up at scrape time; without the pricing collector,
	// the *_monthly_price metrics that need it are left out.
	var prices pricing.Lookup
	if on("pricing") {
		p := pricing.New(client)
		prices = p.Pricing
		add(p)
	}

	// The newest backup per server comes from the images list (no extra requests).
	var images *hcmetrics.ListSource[*hcloud.Image]
	var lastBackups func() map[int64]time.Time
	if on("images") {
		images = image.New(client, prices)
		lastBackups = func() map[int64]time.Time { return image.LastBackups(images.Items()) }
	}

	// Load Balancer targets are labelled with server names if servers are polled.
	var serverName func(int64) string
	if on("servers") {
		servers := server.New(client, concurrency, cfg.MetricsInterval, prices, lastBackups)
		serverName = servers.NameByID
		add(servers)
	}
	if on("load_balancers") {
		add(loadbalancer.New(client, concurrency, cfg.MetricsInterval, serverName, prices))
	}
	if on("certificates") {
		add(certificate.New(client))
	}
	if on("volumes") {
		add(volume.New(client, prices))
	}
	if on("floating_ips") {
		add(floatingip.New(client, prices))
	}
	if on("primary_ips") {
		add(primaryip.New(client, prices))
	}
	if images != nil {
		add(images)
	}
	if on("storage_boxes") {
		add(storagebox.New(client, prices))
	}
	if on("ssh_keys") {
		add(sshkey.New(client))
	}
	return result
}
