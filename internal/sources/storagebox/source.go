// Package storagebox exposes metrics for Hetzner Storage Boxes (served by
// the Hetzner API at api.hetzner.com, with the same token).
package storagebox

import (
	"context"
	"strconv"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcmetrics"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/sources/pricing"
)

var descs = hcmetrics.NewDescSet("storage_box", "storage_box")

var (
	infoDesc          = descs.New("info", "Storage Box information, always 1", "storage_box_id", "storage_box_type", "location", "status", "username")
	quotaDesc         = descs.New("quota_bytes", "Disk quota of the Storage Box type")
	usedDesc          = descs.New("used_bytes", "Used space in total (data and snapshots)")
	usedDataDesc      = descs.New("used_data_bytes", "Space used by data")
	usedSnapshotsDesc = descs.New("used_snapshots_bytes", "Space used by snapshots")
	accessDesc        = descs.New("access_enabled",
		"Whether an access setting is enabled (1=enabled): ssh, samba, webdav, external (reachable from outside Hetzner), zfs (snapshot folder visible)",
		"method")
	protectionDesc = descs.New("delete_protection", "Whether delete protection is enabled (1=enabled)")
	createdDesc    = descs.New("created_timestamp_seconds", "Unix timestamp when the Storage Box was created")
	priceDesc      = descs.New("monthly_price", "Net monthly price of the Storage Box", "currency")
)

// New returns the Storage Boxes source. prices gives the account's currency
// and may be nil.
func New(client *hcloud.Client, prices pricing.Lookup) *hcmetrics.ListSource[*hcloud.StorageBox] {
	return hcmetrics.NewListSource("storage_boxes", descs,
		func(ctx context.Context) ([]*hcloud.StorageBox, error) { return client.StorageBox.All(ctx) },
		func(ch chan<- prometheus.Metric, box *hcloud.StorageBox) { collect(ch, box, prices) })
}

func collect(ch chan<- prometheus.Metric, box *hcloud.StorageBox, prices pricing.Lookup) {
	currency, currencyKnown := prices.Currency()
	var boxType, location string
	if box.Location != nil {
		location = box.Location.Name
	}
	gauge := func(desc *prometheus.Desc, value float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, value, append([]string{box.Name}, labels...)...)
	}

	if t := box.StorageBoxType; t != nil {
		boxType = t.Name
		gauge(quotaDesc, float64(t.Size))
		for _, p := range t.Pricings {
			if p.Location == location && currencyKnown {
				if price, ok := hcmetrics.ParsePrice(p.PriceMonthly.Net); ok {
					gauge(priceDesc, price, currency)
				}
			}
		}
	}

	gauge(infoDesc, 1, strconv.FormatInt(box.ID, 10), boxType, location, string(box.Status), box.Username)
	gauge(usedDesc, float64(box.Stats.Size))
	gauge(usedDataDesc, float64(box.Stats.SizeData))
	gauge(usedSnapshotsDesc, float64(box.Stats.SizeSnapshots))
	for method, enabled := range map[string]bool{
		"ssh":      box.AccessSettings.SSHEnabled,
		"samba":    box.AccessSettings.SambaEnabled,
		"webdav":   box.AccessSettings.WebDAVEnabled,
		"zfs":      box.AccessSettings.ZFSEnabled,
		"external": box.AccessSettings.ReachableExternally,
	} {
		gauge(accessDesc, hcmetrics.BoolToFloat(enabled), method)
	}
	gauge(protectionDesc, hcmetrics.BoolToFloat(box.Protection.Delete))
	if ts, ok := hcmetrics.Timestamp(box.Created); ok {
		gauge(createdDesc, ts)
	}
}
