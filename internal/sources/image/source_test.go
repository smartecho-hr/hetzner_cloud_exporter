package image

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcloudtest"
)

func TestCollect(t *testing.T) {
	_, client := hcloudtest.NewServer(t, map[string]string{
		"/images": `{"images": [
			{"id": 1, "type": "snapshot", "status": "available", "description": "before-upgrade", "image_size": 10.0, "disk_size": 80,
			 "created": "2026-09-01T00:00:00Z", "created_from": {"id": 10, "name": "web-1"}, "bound_to": null, "protection": {"delete": false}},
			{"id": 2, "type": "backup", "status": "available", "description": "", "image_size": 20.0, "disk_size": 80,
			 "created": "2026-10-01T00:00:00Z", "created_from": {"id": 11, "name": "db-1"}, "bound_to": 11, "protection": {"delete": false}}
		], ` + hcloudtest.Pagination + `}`,
	})
	source := New(client, hcloudtest.Prices)
	if err := source.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Only snapshots have a price; backups are part of the server's backup price.
	expected := `
# HELP hetzner_cloud_image_info Snapshot or backup information, always 1
# TYPE hetzner_cloud_image_info gauge
hetzner_cloud_image_info{image="before-upgrade",image_id="1",image_type="snapshot",server="web-1",server_id="10",status="available"} 1
hetzner_cloud_image_info{image="2",image_id="2",image_type="backup",server="db-1",server_id="11",status="available"} 1
# HELP hetzner_cloud_image_monthly_price Net monthly price of a snapshot (backups are part of the server's backup price)
# TYPE hetzner_cloud_image_monthly_price gauge
hetzner_cloud_image_monthly_price{currency="EUR",image="before-upgrade",image_id="1"} 0.10999999999999999
# HELP hetzner_cloud_image_size_bytes Size of the snapshot or backup (what is billed for snapshots)
# TYPE hetzner_cloud_image_size_bytes gauge
hetzner_cloud_image_size_bytes{image="before-upgrade",image_id="1"} 1.073741824e+10
hetzner_cloud_image_size_bytes{image="2",image_id="2"} 2.147483648e+10
`
	if err := testutil.CollectAndCompare(source, strings.NewReader(expected),
		"hetzner_cloud_image_info", "hetzner_cloud_image_monthly_price", "hetzner_cloud_image_size_bytes"); err != nil {
		t.Error(err)
	}
	if problems, err := testutil.CollectAndLint(source); err != nil || len(problems) > 0 {
		t.Errorf("lint: err=%v problems=%v", err, problems)
	}
}

func TestDuplicateDescriptions(t *testing.T) {
	// Descriptions aren't unique ("nightly" snapshots): the id label keeps
	// the series apart.
	_, client := hcloudtest.NewServer(t, map[string]string{
		"/images": `{"images": [
			{"id": 1, "type": "snapshot", "status": "available", "description": "nightly", "image_size": 1.0, "disk_size": 20,
			 "created": "2026-09-01T00:00:00Z", "created_from": {"id": 10, "name": "web-1"}, "protection": {"delete": false}},
			{"id": 2, "type": "snapshot", "status": "available", "description": "nightly", "image_size": 2.0, "disk_size": 20,
			 "created": "2026-09-02T00:00:00Z", "created_from": {"id": 10, "name": "web-1"}, "protection": {"delete": false}}
		], ` + hcloudtest.Pagination + `}`,
	})
	source := New(client, hcloudtest.Prices)
	if err := source.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := testutil.CollectAndCount(source, "hetzner_cloud_image_size_bytes"); got != 2 {
		t.Errorf("size series = %d, want 2", got)
	}
	if problems, err := testutil.CollectAndLint(source); err != nil || len(problems) > 0 {
		t.Errorf("duplicate series: err=%v problems=%v", err, problems)
	}
}

func TestListsOnlySnapshotsAndBackups(t *testing.T) {
	fake, client := hcloudtest.NewServer(t, map[string]string{
		"/images": `{"images": [], ` + hcloudtest.Pagination + `}`,
	})
	if err := New(client, nil).Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	queries := fake.Queries("/images")
	if len(queries) != 1 || !strings.Contains(queries[0], "type=snapshot") || !strings.Contains(queries[0], "type=backup") {
		t.Errorf("queries = %v, want type=snapshot and type=backup", queries)
	}
}

func TestLastBackups(t *testing.T) {
	_, client := hcloudtest.NewServer(t, map[string]string{
		"/images": `{"images": [
			{"id": 1, "type": "backup", "status": "available", "created": "2026-09-30T02:00:00Z", "bound_to": 10, "protection": {"delete": false}},
			{"id": 2, "type": "backup", "status": "available", "created": "2026-10-01T02:00:00Z", "bound_to": 10, "protection": {"delete": false}},
			{"id": 3, "type": "backup", "status": "creating", "created": "2026-10-02T02:00:00Z", "bound_to": 10, "protection": {"delete": false}},
			{"id": 4, "type": "snapshot", "status": "available", "created": "2026-10-02T03:00:00Z", "created_from": {"id": 10, "name": "web-1"}, "protection": {"delete": false}},
			{"id": 5, "type": "backup", "status": "available", "created": "2026-09-01T02:00:00Z", "bound_to": 11, "protection": {"delete": false}}
		], ` + hcloudtest.Pagination + `}`,
	})
	source := New(client, nil)
	if err := source.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Newest available backup per server; backups being created and snapshots don't count.
	got := LastBackups(source.Items())
	want := map[int64]string{10: "2026-10-01T02:00:00Z", 11: "2026-09-01T02:00:00Z"}
	if len(got) != len(want) {
		t.Fatalf("LastBackups = %v, want servers 10 and 11", got)
	}
	for id, ts := range want {
		if got[id].Format(time.RFC3339) != ts {
			t.Errorf("server %d: %v, want %s", id, got[id], ts)
		}
	}
}

// server and server_id always describe the same server.
func TestServerOf(t *testing.T) {
	web1 := &hcloud.Server{ID: 10, Name: "web-1"}
	tests := []struct {
		name     string
		img      *hcloud.Image
		wantID   int64
		wantName string
		wantOK   bool
	}{
		{"snapshot", &hcloud.Image{CreatedFrom: web1}, 10, "web-1", true},
		{"backup with created_from", &hcloud.Image{BoundTo: &hcloud.Server{ID: 10}, CreatedFrom: web1}, 10, "web-1", true},
		{"backup without created_from", &hcloud.Image{BoundTo: &hcloud.Server{ID: 10}}, 10, "", true},
		{"created_from another server", &hcloud.Image{BoundTo: &hcloud.Server{ID: 11}, CreatedFrom: web1}, 11, "", true},
		{"no server", &hcloud.Image{}, 0, "", false},
	}
	for _, tt := range tests {
		id, name, ok := serverOf(tt.img)
		if id != tt.wantID || name != tt.wantName || ok != tt.wantOK {
			t.Errorf("%s: got %d %q %v, want %d %q %v", tt.name, id, name, ok, tt.wantID, tt.wantName, tt.wantOK)
		}
	}
}
