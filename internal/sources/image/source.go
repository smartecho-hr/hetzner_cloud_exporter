// Package image exposes metrics for Hetzner Cloud snapshots and backups
// (Hetzner's own system images are left out).
package image

import (
	"context"
	"strconv"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcmetrics"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/sources/pricing"
)

var descs = hcmetrics.NewDescSet("image", "image")

var (
	infoDesc = descs.New("info", "Snapshot or backup information, always 1",
		"image_id", "image_type", "status", "server", "server_id")
	// "image" is the description, which isn't unique (e.g. "nightly"), so
	// every metric also carries image_id.
	sizeDesc       = descs.New("size_bytes", "Size of the snapshot or backup (what is billed for snapshots)", "image_id")
	diskSizeDesc   = descs.New("disk_size_bytes", "Size of the disk the image was created from", "image_id")
	protectionDesc = descs.New("delete_protection", "Whether delete protection is enabled (1=enabled)", "image_id")
	createdDesc    = descs.New("created_timestamp_seconds", "Unix timestamp when the snapshot or backup was created", "image_id")
	priceDesc      = descs.New("monthly_price", "Net monthly price of a snapshot (backups are part of the server's backup price)", "image_id", "currency")
)

// New returns the images source for snapshots and backups. prices may be nil.
func New(client *hcloud.Client, prices pricing.Lookup) *hcmetrics.ListSource[*hcloud.Image] {
	return hcmetrics.NewListSource("images", descs,
		func(ctx context.Context) ([]*hcloud.Image, error) {
			return client.Image.AllWithOpts(ctx, hcloud.ImageListOpts{
				Type: []hcloud.ImageType{hcloud.ImageTypeSnapshot, hcloud.ImageTypeBackup},
			})
		},
		func(ch chan<- prometheus.Metric, img *hcloud.Image) { collect(ch, img, prices) })
}

// LastBackups returns the creation time of the newest available backup per
// server ID. Backups are bound to their server; images still being created
// don't count.
func LastBackups(images []*hcloud.Image) map[int64]time.Time {
	last := make(map[int64]time.Time)
	for _, img := range images {
		if img.Type != hcloud.ImageTypeBackup || img.Status != hcloud.ImageStatusAvailable {
			continue
		}
		serverID, _, ok := serverOf(img)
		if !ok {
			continue
		}
		if img.Created.After(last[serverID]) {
			last[serverID] = img.Created
		}
	}
	return last
}

// serverOf returns the server an image belongs to: the one a backup is
// bound to, or the one a snapshot was created from. The API gives only the
// ID in bound_to, so the name comes from created_from when it is the same
// server.
func serverOf(img *hcloud.Image) (id int64, name string, ok bool) {
	switch {
	case img.BoundTo != nil:
		if img.CreatedFrom != nil && img.CreatedFrom.ID == img.BoundTo.ID {
			name = img.CreatedFrom.Name
		}
		return img.BoundTo.ID, name, true
	case img.CreatedFrom != nil:
		return img.CreatedFrom.ID, img.CreatedFrom.Name, true
	}
	return 0, "", false
}

// imageName labels an image: its description, or its ID if it has none.
func imageName(img *hcloud.Image) string {
	if img.Description != "" {
		return img.Description
	}
	return strconv.FormatInt(img.ID, 10)
}

func collect(ch chan<- prometheus.Metric, img *hcloud.Image, prices pricing.Lookup) {
	var server, serverID string
	if sid, name, ok := serverOf(img); ok {
		server, serverID = name, strconv.FormatInt(sid, 10)
	}
	name := imageName(img)
	id := strconv.FormatInt(img.ID, 10)
	gauge := func(desc *prometheus.Desc, value float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, value, append([]string{name, id}, labels...)...)
	}

	ch <- prometheus.MustNewConstMetric(infoDesc, prometheus.GaugeValue, 1,
		name, id, string(img.Type), string(img.Status), server, serverID)
	gauge(sizeDesc, float64(img.ImageSize)*hcmetrics.Gibibyte) // the API reports GB
	gauge(diskSizeDesc, float64(img.DiskSize)*hcmetrics.Gibibyte)
	gauge(protectionDesc, hcmetrics.BoolToFloat(img.Protection.Delete))
	if ts, ok := hcmetrics.Timestamp(img.Created); ok {
		gauge(createdDesc, ts)
	}
	if img.Type == hcloud.ImageTypeSnapshot {
		if perGB, ok := prices.SnapshotPerGB(); ok {
			gauge(priceDesc, float64(img.ImageSize)*perGB, currency(prices))
		}
	}
}

// currency returns the account's currency; prices from the price list are
// only found once it was fetched, so the currency is known then.
func currency(prices pricing.Lookup) string {
	c, _ := prices.Currency()
	return c
}
