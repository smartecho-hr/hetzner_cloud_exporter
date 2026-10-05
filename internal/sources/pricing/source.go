// Package pricing fetches Hetzner's price list (hourly by default) for the cost metrics
// of other sources. Prices are net (without VAT) in the account's currency.
package pricing

import (
	"context"
	"fmt"
	"sync"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcmetrics"
)

var descs = hcmetrics.NewDescSet("price", "currency")

var (
	volumeDesc   = descs.New("volume_per_gb_monthly", "Net price per GB of volume per month")
	snapshotDesc = descs.New("snapshot_per_gb_monthly", "Net price per GB of snapshot per month")
	backupDesc   = descs.New("server_backup_percent", "Price of server backups in % of the server price")
)

// Source exposes the price list and makes it available to other sources.
type Source struct {
	client *hcloud.Client

	mu      sync.RWMutex
	pricing *hcloud.Pricing
}

// New returns the pricing source. Its Pricing method is the Lookup other
// sources use for their price metrics.
func New(client *hcloud.Client) *Source { return &Source{client: client} }

func (s *Source) Name() string { return "pricing" }

func (s *Source) Fetch(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, hcmetrics.RequestTimeout)
	defer cancel()

	pricing, _, err := s.client.Pricing.Get(ctx)
	if err != nil {
		return fmt.Errorf("fetching pricing: %w", err)
	}

	s.mu.Lock()
	s.pricing = &pricing
	s.mu.Unlock()
	return nil
}

// Pricing returns the last fetched price list, or nil before the first fetch.
func (s *Source) Pricing() *hcloud.Pricing {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pricing
}

func (s *Source) Describe(ch chan<- *prometheus.Desc) { descs.Describe(ch) }

func (s *Source) Collect(ch chan<- prometheus.Metric) {
	p := s.Pricing()
	if p == nil {
		return
	}
	gauge := func(desc *prometheus.Desc, price string) {
		if value, ok := hcmetrics.ParsePrice(price); ok {
			ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, value, p.Currency)
		}
	}
	gauge(volumeDesc, p.Volume.PerGBMonthly.Net)
	gauge(snapshotDesc, p.Image.PerGBMonth.Net)
	gauge(backupDesc, p.ServerBackup.Percentage)
}

// Lookup returns prices from the last fetched price list. A nil Lookup or
// one without data returns ok=false for every price.
type Lookup func() *hcloud.Pricing

// VolumePerGB returns the net monthly price per GB of volume.
func (l Lookup) VolumePerGB() (float64, bool) {
	if p := l.get(); p != nil {
		return hcmetrics.ParsePrice(p.Volume.PerGBMonthly.Net)
	}
	return 0, false
}

// SnapshotPerGB returns the net monthly price per GB of snapshot.
func (l Lookup) SnapshotPerGB() (float64, bool) {
	if p := l.get(); p != nil {
		return hcmetrics.ParsePrice(p.Image.PerGBMonth.Net)
	}
	return 0, false
}

// BackupPercent returns the backup surcharge in % of the server price.
func (l Lookup) BackupPercent() (float64, bool) {
	if p := l.get(); p != nil {
		return hcmetrics.ParsePrice(p.ServerBackup.Percentage)
	}
	return 0, false
}

// PrimaryIP returns the net monthly price of a primary IP of the given type
// ("ipv4", "ipv6") in a location.
func (l Lookup) PrimaryIP(ipType, location string) (float64, bool) {
	p := l.get()
	if p == nil {
		return 0, false
	}
	for _, t := range p.PrimaryIPs {
		if t.Type != ipType {
			continue
		}
		for _, lp := range t.Pricings {
			if lp.Location == location {
				return hcmetrics.ParsePrice(lp.Monthly.Net)
			}
		}
	}
	return 0, false
}

// FloatingIP returns the net monthly price of a floating IP of the given type in a location.
func (l Lookup) FloatingIP(ipType hcloud.FloatingIPType, location string) (float64, bool) {
	p := l.get()
	if p == nil {
		return 0, false
	}
	for _, t := range p.FloatingIPs {
		if t.Type != ipType {
			continue
		}
		for _, lp := range t.Pricings {
			if lp.Location != nil && lp.Location.Name == location {
				return hcmetrics.ParsePrice(lp.Monthly.Net)
			}
		}
	}
	return 0, false
}

// DefaultCurrency is assumed for prices when the pricing collector is off
// (no price list, so the account's currency is unknown).
const DefaultCurrency = "EUR"

// Currency returns the currency of the account's prices (ISO 4217, e.g.
// "EUR" or "USD"). With the pricing collector off (nil Lookup) it returns
// DefaultCurrency; with it on but before the first fetch it returns false,
// so prices aren't exported with a currency that changes once it is known.
func (l Lookup) Currency() (string, bool) {
	if l == nil {
		return DefaultCurrency, true
	}
	p := l()
	switch {
	case p == nil:
		return "", false
	case p.Currency == "":
		return DefaultCurrency, true
	}
	return p.Currency, true
}

func (l Lookup) get() *hcloud.Pricing {
	if l == nil {
		return nil
	}
	return l()
}
