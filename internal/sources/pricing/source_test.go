package pricing

import (
	"context"
	"strings"
	"testing"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcloudtest"
)

func TestSource(t *testing.T) {
	_, client := hcloudtest.NewServer(t, map[string]string{
		"/pricing": `{"pricing": {"currency": "EUR", "vat_rate": "19.00",
			"image": {"price_per_gb_month": {"net": "0.0110", "gross": "0.0131"}},
			"volume": {"price_per_gb_month": {"net": "0.0440", "gross": "0.0524"}},
			"server_backup": {"percentage": "20.00"},
			"primary_ips": [{"type": "ipv4", "prices": [{"location": "fsn1",
				"price_hourly": {"net": "0.0008", "gross": "0.001"}, "price_monthly": {"net": "0.5000", "gross": "0.595"}}]}],
			"floating_ips": [{"type": "ipv4", "prices": [{"location": "fsn1", "price_monthly": {"net": "3.0000", "gross": "3.57"}}]}],
			"server_types": [], "load_balancer_types": [], "traffic": {"price_per_tb": {"net": "1", "gross": "1"}}}}`,
	})
	source := New(client)

	if source.Pricing() != nil {
		t.Fatal("Pricing before first fetch should be nil")
	}
	if err := source.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	expected := `
# HELP hetzner_cloud_price_server_backup_percent Price of server backups in % of the server price
# TYPE hetzner_cloud_price_server_backup_percent gauge
hetzner_cloud_price_server_backup_percent{currency="EUR"} 20
# HELP hetzner_cloud_price_snapshot_per_gb_monthly Net price per GB of snapshot per month
# TYPE hetzner_cloud_price_snapshot_per_gb_monthly gauge
hetzner_cloud_price_snapshot_per_gb_monthly{currency="EUR"} 0.011
# HELP hetzner_cloud_price_volume_per_gb_monthly Net price per GB of volume per month
# TYPE hetzner_cloud_price_volume_per_gb_monthly gauge
hetzner_cloud_price_volume_per_gb_monthly{currency="EUR"} 0.044
`
	if err := testutil.CollectAndCompare(source, strings.NewReader(expected)); err != nil {
		t.Error(err)
	}

	lookup := Lookup(source.Pricing)
	if v, ok := lookup.PrimaryIP("ipv4", "fsn1"); !ok || v != 0.5 {
		t.Errorf("PrimaryIP(ipv4, fsn1) = %v, %v", v, ok)
	}
	if _, ok := lookup.PrimaryIP("ipv6", "fsn1"); ok {
		t.Error("PrimaryIP(ipv6) should have no price")
	}
	if v, ok := lookup.FloatingIP(hcloud.FloatingIPTypeIPv4, "fsn1"); !ok || v != 3 {
		t.Errorf("FloatingIP(ipv4, fsn1) = %v, %v", v, ok)
	}
	if _, ok := lookup.FloatingIP(hcloud.FloatingIPTypeIPv4, "nbg1"); ok {
		t.Error("FloatingIP in unknown location should have no price")
	}
}

func TestNilLookup(t *testing.T) {
	var lookup Lookup
	if _, ok := lookup.VolumePerGB(); ok {
		t.Error("nil Lookup should return no prices")
	}
	empty := Lookup(func() *hcloud.Pricing { return nil })
	if _, ok := empty.BackupPercent(); ok {
		t.Error("Lookup without data should return no prices")
	}
}

func TestNoDataBeforeFetch(t *testing.T) {
	_, client := hcloudtest.NewServer(t, map[string]string{})
	source := New(client)
	if got := testutil.CollectAndCount(source); got != 0 {
		t.Errorf("series before the first fetch = %d, want 0", got)
	}
	lookup := Lookup(source.Pricing)
	if _, ok := lookup.SnapshotPerGB(); ok {
		t.Error("SnapshotPerGB without data should not be ok")
	}
	if _, ok := lookup.PrimaryIP("ipv4", "fsn1"); ok {
		t.Error("PrimaryIP without data should not be ok")
	}
	if _, ok := lookup.FloatingIP(hcloud.FloatingIPTypeIPv4, "fsn1"); ok {
		t.Error("FloatingIP without data should not be ok")
	}
	if got, ok := lookup.Currency(); ok {
		t.Errorf("Currency before the first fetch = %q, want unknown", got)
	}
	if got, ok := Lookup(nil).Currency(); !ok || got != DefaultCurrency {
		t.Errorf("Currency with pricing off = %q %v, want %q", got, ok, DefaultCurrency)
	}
	if err := source.Fetch(context.Background()); err == nil {
		t.Error("Fetch from an API without /pricing should fail")
	}
}
