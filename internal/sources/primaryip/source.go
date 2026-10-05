// Package primaryip exposes metrics for Hetzner Cloud primary IPs.
package primaryip

import (
	"context"
	"strconv"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcmetrics"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/sources/pricing"
)

var descs = hcmetrics.NewDescSet("primary_ip", "primary_ip")

var (
	infoDesc       = descs.New("info", "Primary IP information, always 1", "primary_ip_id", "ip", "primary_ip_type", "location", "server_id")
	assignedDesc   = descs.New("assigned", "Whether the primary IP is assigned to a server (1=assigned); unassigned IPv4 still costs money")
	autoDeleteDesc = descs.New("auto_delete", "Whether the IP is deleted together with its server (1=yes)")
	blockedDesc    = descs.New("blocked", "Whether Hetzner blocked the IP, e.g. after abuse (1=blocked)")
	protectionDesc = descs.New("delete_protection", "Whether delete protection is enabled (1=enabled)")
	createdDesc    = descs.New("created_timestamp_seconds", "Unix timestamp when the primary IP was created")
	priceDesc      = descs.New("monthly_price", "Net monthly price of the primary IP (IPv6 is free)", "currency")
)

// New returns the primary IPs source. prices may be nil.
func New(client *hcloud.Client, prices pricing.Lookup) *hcmetrics.ListSource[*hcloud.PrimaryIP] {
	return hcmetrics.NewListSource("primary_ips", descs,
		func(ctx context.Context) ([]*hcloud.PrimaryIP, error) { return client.PrimaryIP.All(ctx) },
		func(ch chan<- prometheus.Metric, ip *hcloud.PrimaryIP) { collect(ch, ip, prices) })
}

func collect(ch chan<- prometheus.Metric, ip *hcloud.PrimaryIP, prices pricing.Lookup) {
	var location, address, assignee string
	if ip.Location != nil {
		location = ip.Location.Name
	}
	if ip.IP != nil {
		address = ip.IP.String()
	}
	if ip.AssigneeID != 0 {
		assignee = strconv.FormatInt(ip.AssigneeID, 10)
	}
	gauge := func(desc *prometheus.Desc, value float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, value, append([]string{ip.Name}, labels...)...)
	}

	gauge(infoDesc, 1, strconv.FormatInt(ip.ID, 10), address, string(ip.Type), location, assignee)
	gauge(assignedDesc, hcmetrics.BoolToFloat(ip.AssigneeID != 0))
	gauge(autoDeleteDesc, hcmetrics.BoolToFloat(ip.AutoDelete))
	gauge(blockedDesc, hcmetrics.BoolToFloat(ip.Blocked))
	gauge(protectionDesc, hcmetrics.BoolToFloat(ip.Protection.Delete))
	if ts, ok := hcmetrics.Timestamp(ip.Created); ok {
		gauge(createdDesc, ts)
	}
	if price, ok := prices.PrimaryIP(string(ip.Type), location); ok {
		gauge(priceDesc, price, currency(prices))
	}
}

// currency returns the account's currency; prices from the price list are
// only found once it was fetched, so the currency is known then.
func currency(prices pricing.Lookup) string {
	c, _ := prices.Currency()
	return c
}
