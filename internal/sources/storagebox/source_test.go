package storagebox

import (
	"context"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcloudtest"
)

func TestCollect(t *testing.T) {
	fake, client := hcloudtest.NewServer(t, map[string]string{
		"/storage_boxes": `{"storage_boxes": [{"id": 1, "name": "backups", "username": "u1", "status": "active",
			"storage_box_type": {"name": "bx11", "size": 1099511627776, "subaccounts_limit": 100,
				"prices": [{"location": "fsn1", "price_hourly": {"net": "0.005", "gross": "0.006"},
					"price_monthly": {"net": "3.2000", "gross": "3.81"}, "setup_fee": {"net": "0", "gross": "0"}}]},
			"location": {"name": "fsn1"},
			"access_settings": {"reachable_externally": false, "samba_enabled": false, "ssh_enabled": true, "webdav_enabled": false, "zfs_enabled": true},
			"stats": {"size": 600000000000, "size_data": 500000000000, "size_snapshots": 100000000000},
			"protection": {"delete": true}, "created": "2026-01-01T00:00:00Z", "labels": {}}],
			` + hcloudtest.Pagination + `}`,
	})
	source := New(client, nil)
	if err := source.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if fake.RequestCount("/storage_boxes") != 1 {
		t.Error("storage boxes were not requested from the Hetzner endpoint")
	}

	expected := `
# HELP hetzner_cloud_storage_box_access_enabled Whether an access setting is enabled (1=enabled): ssh, samba, webdav, external (reachable from outside Hetzner), zfs (snapshot folder visible)
# TYPE hetzner_cloud_storage_box_access_enabled gauge
hetzner_cloud_storage_box_access_enabled{method="external",storage_box="backups"} 0
hetzner_cloud_storage_box_access_enabled{method="samba",storage_box="backups"} 0
hetzner_cloud_storage_box_access_enabled{method="ssh",storage_box="backups"} 1
hetzner_cloud_storage_box_access_enabled{method="webdav",storage_box="backups"} 0
hetzner_cloud_storage_box_access_enabled{method="zfs",storage_box="backups"} 1
# HELP hetzner_cloud_storage_box_monthly_price Net monthly price of the Storage Box
# TYPE hetzner_cloud_storage_box_monthly_price gauge
hetzner_cloud_storage_box_monthly_price{currency="EUR",storage_box="backups"} 3.2
# HELP hetzner_cloud_storage_box_quota_bytes Disk quota of the Storage Box type
# TYPE hetzner_cloud_storage_box_quota_bytes gauge
hetzner_cloud_storage_box_quota_bytes{storage_box="backups"} 1.099511627776e+12
# HELP hetzner_cloud_storage_box_used_bytes Used space in total (data and snapshots)
# TYPE hetzner_cloud_storage_box_used_bytes gauge
hetzner_cloud_storage_box_used_bytes{storage_box="backups"} 6e+11
`
	if err := testutil.CollectAndCompare(source, strings.NewReader(expected),
		"hetzner_cloud_storage_box_access_enabled", "hetzner_cloud_storage_box_monthly_price",
		"hetzner_cloud_storage_box_quota_bytes", "hetzner_cloud_storage_box_used_bytes"); err != nil {
		t.Error(err)
	}
	if problems, err := testutil.CollectAndLint(source); err != nil || len(problems) > 0 {
		t.Errorf("lint: err=%v problems=%v", err, problems)
	}
}
