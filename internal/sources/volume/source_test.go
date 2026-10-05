package volume

import (
	"context"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcloudtest"
)

func TestCollect(t *testing.T) {
	_, client := hcloudtest.NewServer(t, map[string]string{
		"/volumes": `{"volumes": [
			{"id": 1, "name": "data", "status": "available", "size": 100, "server": 10, "location": {"name": "fsn1"},
			 "protection": {"delete": true}, "created": "2026-01-01T00:00:00Z"},
			{"id": 2, "name": "old", "status": "available", "size": 50, "server": null, "location": {"name": "fsn1"},
			 "protection": {"delete": false}, "created": "2025-01-01T00:00:00Z"}
		], ` + hcloudtest.Pagination + `}`,
	})
	source := New(client, hcloudtest.Prices)
	if err := source.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	expected := `
# HELP hetzner_cloud_volume_attached Whether the volume is attached to a server (1=attached); unattached volumes still cost money
# TYPE hetzner_cloud_volume_attached gauge
hetzner_cloud_volume_attached{volume="data"} 1
hetzner_cloud_volume_attached{volume="old"} 0
# HELP hetzner_cloud_volume_info Volume information, always 1
# TYPE hetzner_cloud_volume_info gauge
hetzner_cloud_volume_info{location="fsn1",server_id="10",status="available",volume="data",volume_id="1"} 1
hetzner_cloud_volume_info{location="fsn1",server_id="",status="available",volume="old",volume_id="2"} 1
# HELP hetzner_cloud_volume_monthly_price Net monthly price of the volume
# TYPE hetzner_cloud_volume_monthly_price gauge
hetzner_cloud_volume_monthly_price{currency="EUR",volume="data"} 4.3999999999999995
hetzner_cloud_volume_monthly_price{currency="EUR",volume="old"} 2.1999999999999997
# HELP hetzner_cloud_volume_size_bytes Size of the volume
# TYPE hetzner_cloud_volume_size_bytes gauge
hetzner_cloud_volume_size_bytes{volume="data"} 1.073741824e+11
hetzner_cloud_volume_size_bytes{volume="old"} 5.36870912e+10
`
	err := testutil.CollectAndCompare(source, strings.NewReader(expected),
		"hetzner_cloud_volume_attached", "hetzner_cloud_volume_info", "hetzner_cloud_volume_monthly_price", "hetzner_cloud_volume_size_bytes")
	if err != nil {
		t.Error(err)
	}
	if problems, err := testutil.CollectAndLint(source); err != nil || len(problems) > 0 {
		t.Errorf("lint: err=%v problems=%v", err, problems)
	}
}
