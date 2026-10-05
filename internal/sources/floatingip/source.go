// Package floatingip exposes metrics for Hetzner Cloud floating IPs.
package floatingip

import (
	"context"
	"strconv"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcmetrics"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/sources/pricing"
)

var descs = hcmetrics.NewDescSet("floating_ip", "floating_ip")

var (
	infoDesc       = descs.New("info", "Floating IP information, always 1", "floating_ip_id", "ip", "floating_ip_type", "location", "server_id")
	assignedDesc   = descs.New("assigned", "Whether the floating IP is assigned to a server (1=assigned); unassigned IPs still cost money")
	blockedDesc    = descs.New("blocked", "Whether Hetzner blocked the IP, e.g. after abuse (1=blocked)")
	protectionDesc = descs.New("delete_protection", "Whether delete protection is enabled (1=enabled)")
	createdDesc    = descs.New("created_timestamp_seconds", "Unix timestamp when the floating IP was created")
	priceDesc      = descs.New("monthly_price", "Net monthly price of the floating IP", "currency")
)

// New returns the floating IPs source. prices may be nil.
func New(client *hcloud.Client, prices pricing.Lookup) *hcmetrics.ListSource[*hcloud.FloatingIP] {
	return hcmetrics.NewListSource("floating_ips", descs,
		func(ctx context.Context) ([]*hcloud.FloatingIP, error) { return client.FloatingIP.All(ctx) },
		func(ch chan<- prometheus.Metric, ip *hcloud.FloatingIP) { collect(ch, ip, prices) })
}

func collect(ch chan<- prometheus.Metric, ip *hcloud.FloatingIP, prices pricing.Lookup) {
	var location, serverID, address string
	if ip.HomeLocation != nil {
		location = ip.HomeLocation.Name
	}
	if ip.Server != nil {
		serverID = strconv.FormatInt(ip.Server.ID, 10)
	}
	if ip.IP != nil {
		address = ip.IP.String()
	}
	name := ip.Name
	if name == "" {
		name = address
	}
	gauge := func(desc *prometheus.Desc, value float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, value, append([]string{name}, labels...)...)
	}

	gauge(infoDesc, 1, strconv.FormatInt(ip.ID, 10), address, string(ip.Type), location, serverID)
	gauge(assignedDesc, hcmetrics.BoolToFloat(ip.Server != nil))
	gauge(blockedDesc, hcmetrics.BoolToFloat(ip.Blocked))
	gauge(protectionDesc, hcmetrics.BoolToFloat(ip.Protection.Delete))
	if ts, ok := hcmetrics.Timestamp(ip.Created); ok {
		gauge(createdDesc, ts)
	}
	if price, ok := prices.FloatingIP(ip.Type, location); ok {
		gauge(priceDesc, price, currency(prices))
	}
}

// currency returns the account's currency; prices from the price list are
// only found once it was fetched, so the currency is known then.
func currency(prices pricing.Lookup) string {
	c, _ := prices.Currency()
	return c
}
