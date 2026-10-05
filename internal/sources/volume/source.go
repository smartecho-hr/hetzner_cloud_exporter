// Package volume exposes metrics for Hetzner Cloud volumes.
package volume

import (
	"context"
	"strconv"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcmetrics"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/sources/pricing"
)

var descs = hcmetrics.NewDescSet("volume", "volume")

var (
	infoDesc       = descs.New("info", "Volume information, always 1", "volume_id", "location", "status", "server_id")
	sizeDesc       = descs.New("size_bytes", "Size of the volume")
	attachedDesc   = descs.New("attached", "Whether the volume is attached to a server (1=attached); unattached volumes still cost money")
	protectionDesc = descs.New("delete_protection", "Whether delete protection is enabled (1=enabled)")
	createdDesc    = descs.New("created_timestamp_seconds", "Unix timestamp when the volume was created")
	priceDesc      = descs.New("monthly_price", "Net monthly price of the volume", "currency")
)

// New returns the volumes source. prices may be nil.
func New(client *hcloud.Client, prices pricing.Lookup) *hcmetrics.ListSource[*hcloud.Volume] {
	return hcmetrics.NewListSource("volumes", descs,
		func(ctx context.Context) ([]*hcloud.Volume, error) { return client.Volume.All(ctx) },
		func(ch chan<- prometheus.Metric, v *hcloud.Volume) { collect(ch, v, prices) })
}

func collect(ch chan<- prometheus.Metric, v *hcloud.Volume, prices pricing.Lookup) {
	var location, serverID string
	if v.Location != nil {
		location = v.Location.Name
	}
	if v.Server != nil {
		serverID = strconv.FormatInt(v.Server.ID, 10)
	}
	gauge := func(desc *prometheus.Desc, value float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, value, append([]string{v.Name}, labels...)...)
	}

	gauge(infoDesc, 1, strconv.FormatInt(v.ID, 10), location, string(v.Status), serverID)
	gauge(sizeDesc, float64(v.Size)*hcmetrics.Gibibyte) // the API reports GB
	gauge(attachedDesc, hcmetrics.BoolToFloat(v.Server != nil))
	gauge(protectionDesc, hcmetrics.BoolToFloat(v.Protection.Delete))
	if ts, ok := hcmetrics.Timestamp(v.Created); ok {
		gauge(createdDesc, ts)
	}
	if perGB, ok := prices.VolumePerGB(); ok {
		gauge(priceDesc, float64(v.Size)*perGB, currency(prices))
	}
}

// currency returns the account's currency; prices from the price list are
// only found once it was fetched, so the currency is known then.
func currency(prices pricing.Lookup) string {
	c, _ := prices.Currency()
	return c
}
