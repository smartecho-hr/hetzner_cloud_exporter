package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcloudtest"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcmetrics"
)

var responses = map[string]string{
	"/servers": `{"servers": [
		{"id": 10, "name": "web-1", "status": "running", "server_type": {"name": "cx22", "cores": 2},
		 "location": {"name": "fsn1"},
		 "outgoing_traffic": 500, "ingoing_traffic": 600, "included_traffic": 1000},
		{"id": 11, "name": "web-2", "status": "off", "server_type": {"name": "cx22", "cores": 2},
		 "location": {"name": "nbg1"}}
	], ` + hcloudtest.Pagination + `}`,
	"/servers/10/metrics": `{"metrics": {"start": "2026-01-01T00:00:00Z", "end": "2026-01-01T00:01:00Z", "step": 60, "time_series": {
		"cpu": {"values": [[1767225660, "12.5"]]},
		"disk.0.iops.read": {"values": [[1767225660, "3"]]},
		"disk.0.bandwidth.write": {"values": [[1767225660, "4096"]]},
		"network.0.bandwidth.in": {"values": [[1767225660, "100"]]},
		"network.0.pps.out": {"values": [[1767225660, "7"]]},
		"network.0.something.new": {"values": [[1767225660, "1"]]}
	}}}`,
}

func TestCollect(t *testing.T) {
	fake, client := hcloudtest.NewServer(t, responses)
	source := New(client, 2, time.Minute, nil, nil)

	if err := source.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := fake.RequestCount("/servers/11/metrics"); got != 0 {
		t.Errorf("metrics requests for stopped server = %d, want 0", got)
	}

	expected := `
# HELP hetzner_cloud_server_cpu_usage_ratio CPU usage across all vCPUs (0-1, 1 = all vCPUs busy)
# TYPE hetzner_cloud_server_cpu_usage_ratio gauge
hetzner_cloud_server_cpu_usage_ratio{server="web-1"} 0.0625
# HELP hetzner_cloud_server_disk_read_iops Disk read operations per second
# TYPE hetzner_cloud_server_disk_read_iops gauge
hetzner_cloud_server_disk_read_iops{disk="0",server="web-1"} 3
# HELP hetzner_cloud_server_disk_write_bytes_per_second Disk write (bytes/s)
# TYPE hetzner_cloud_server_disk_write_bytes_per_second gauge
hetzner_cloud_server_disk_write_bytes_per_second{disk="0",server="web-1"} 4096
# HELP hetzner_cloud_server_info Server information, always 1
# TYPE hetzner_cloud_server_info gauge
hetzner_cloud_server_info{location="fsn1",server="web-1",server_id="10",server_type="cx22",status="running"} 1
hetzner_cloud_server_info{location="nbg1",server="web-2",server_id="11",server_type="cx22",status="off"} 1
# HELP hetzner_cloud_server_network_in_bytes_per_second Public network inbound traffic (bytes/s)
# TYPE hetzner_cloud_server_network_in_bytes_per_second gauge
hetzner_cloud_server_network_in_bytes_per_second{interface="0",server="web-1"} 100
# HELP hetzner_cloud_server_network_out_packets_per_second Public network outbound packets per second
# TYPE hetzner_cloud_server_network_out_packets_per_second gauge
hetzner_cloud_server_network_out_packets_per_second{interface="0",server="web-1"} 7
# HELP hetzner_cloud_server_outgoing_traffic_bytes Outbound traffic in the current billing period (bytes)
# TYPE hetzner_cloud_server_outgoing_traffic_bytes gauge
hetzner_cloud_server_outgoing_traffic_bytes{server="web-1"} 500
hetzner_cloud_server_outgoing_traffic_bytes{server="web-2"} 0
# HELP hetzner_cloud_server_running Whether the server is running (1=running, 0=any other status)
# TYPE hetzner_cloud_server_running gauge
hetzner_cloud_server_running{server="web-1"} 1
hetzner_cloud_server_running{server="web-2"} 0
`
	err := testutil.CollectAndCompare(source, strings.NewReader(expected),
		"hetzner_cloud_server_cpu_usage_ratio",
		"hetzner_cloud_server_disk_read_iops",
		"hetzner_cloud_server_disk_write_bytes_per_second",
		"hetzner_cloud_server_info",
		"hetzner_cloud_server_network_in_bytes_per_second",
		"hetzner_cloud_server_network_out_packets_per_second",
		"hetzner_cloud_server_outgoing_traffic_bytes",
		"hetzner_cloud_server_running",
	)
	if err != nil {
		t.Error(err)
	}

	if problems, err := testutil.CollectAndLint(source); err != nil || len(problems) > 0 {
		t.Errorf("lint: err=%v problems=%v", err, problems)
	}
}

func TestFetchKeepsServersWhenMetricsFail(t *testing.T) {
	fake, client := hcloudtest.NewServer(t, responses)
	fake.Fail("/servers/10/metrics")
	source := New(client, 2, time.Minute, nil, nil)

	// A failed metrics request is a partial error: the list is fresh.
	if err := source.Fetch(context.Background()); !hcmetrics.IsPartial(err) {
		t.Fatalf("err = %v, want a partial error", err)
	}
	if got := testutil.CollectAndCount(source, "hetzner_cloud_server_info"); got != 2 {
		t.Errorf("info series = %d, want 2", got)
	}
	if got := testutil.CollectAndCount(source, "hetzner_cloud_server_cpu_usage_ratio"); got != 0 {
		t.Errorf("cpu series = %d, want 0", got)
	}
}

func TestNameByID(t *testing.T) {
	_, client := hcloudtest.NewServer(t, responses)
	source := New(client, 2, time.Minute, nil, nil)

	if got := source.NameByID(10); got != "" {
		t.Errorf("before first poll: NameByID(10) = %q, want empty", got)
	}
	if err := source.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := source.NameByID(10); got != "web-1" {
		t.Errorf("NameByID(10) = %q, want web-1", got)
	}
	if got := source.NameByID(99); got != "" {
		t.Errorf("NameByID(99) = %q, want empty", got)
	}
}

func TestCollectServerDetails(t *testing.T) {
	_, client := hcloudtest.NewServer(t, map[string]string{
		"/servers": `{"servers": [
			{"id": 20, "name": "db-1", "status": "off", "location": {"name": "fsn1"},
			 "primary_disk_size": 80, "locked": true, "rescue_enabled": true, "backup_window": "22-02",
			 "protection": {"delete": true, "rebuild": false},
			 "server_type": {"name": "cx32", "cores": 4, "memory": 8.0, "disk": 80, "locations": [
				{"name": "fsn1", "deprecation": {"announced": "2026-01-01T00:00:00Z", "unavailable_after": "2026-12-01T00:00:00Z"}},
				{"name": "nbg1", "deprecation": null}
			 ]}},
			{"id": 21, "name": "db-2", "status": "off", "location": {"name": "nbg1"},
			 "primary_disk_size": 40, "protection": {"delete": false, "rebuild": true},
			 "server_type": {"name": "cx32", "cores": 4, "memory": 8.0, "disk": 80, "locations": [
				{"name": "fsn1", "deprecation": {"announced": "2026-01-01T00:00:00Z", "unavailable_after": "2026-12-01T00:00:00Z"}},
				{"name": "nbg1", "deprecation": null}
			 ]}}
		], ` + hcloudtest.Pagination + `}`,
	})
	source := New(client, 2, time.Minute, nil, nil)
	if err := source.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	expected := `
# HELP hetzner_cloud_server_backup_enabled Whether backups are enabled (1=enabled)
# TYPE hetzner_cloud_server_backup_enabled gauge
hetzner_cloud_server_backup_enabled{server="db-1"} 1
hetzner_cloud_server_backup_enabled{server="db-2"} 0
# HELP hetzner_cloud_server_cpu_cores Number of vCPUs of the server type
# TYPE hetzner_cloud_server_cpu_cores gauge
hetzner_cloud_server_cpu_cores{server="db-1"} 4
hetzner_cloud_server_cpu_cores{server="db-2"} 4
# HELP hetzner_cloud_server_delete_protection Whether delete protection is enabled (1=enabled)
# TYPE hetzner_cloud_server_delete_protection gauge
hetzner_cloud_server_delete_protection{server="db-1"} 1
hetzner_cloud_server_delete_protection{server="db-2"} 0
# HELP hetzner_cloud_server_disk_bytes Size of the primary disk (bytes)
# TYPE hetzner_cloud_server_disk_bytes gauge
hetzner_cloud_server_disk_bytes{server="db-1"} 8.589934592e+10
hetzner_cloud_server_disk_bytes{server="db-2"} 4.294967296e+10
# HELP hetzner_cloud_server_locked Whether the server is locked by Hetzner, e.g. during a running action (1=locked)
# TYPE hetzner_cloud_server_locked gauge
hetzner_cloud_server_locked{server="db-1"} 1
hetzner_cloud_server_locked{server="db-2"} 0
# HELP hetzner_cloud_server_memory_bytes Memory of the server type (bytes)
# TYPE hetzner_cloud_server_memory_bytes gauge
hetzner_cloud_server_memory_bytes{server="db-1"} 8.589934592e+09
hetzner_cloud_server_memory_bytes{server="db-2"} 8.589934592e+09
# HELP hetzner_cloud_server_type_deprecated Whether the server type is deprecated in the server's location (1=deprecated)
# TYPE hetzner_cloud_server_type_deprecated gauge
hetzner_cloud_server_type_deprecated{server="db-1"} 1
hetzner_cloud_server_type_deprecated{server="db-2"} 0
# HELP hetzner_cloud_server_type_unavailable_after_timestamp_seconds Unix timestamp after which the deprecated server type can no longer be ordered in the server's location
# TYPE hetzner_cloud_server_type_unavailable_after_timestamp_seconds gauge
hetzner_cloud_server_type_unavailable_after_timestamp_seconds{server="db-1"} 1.7960832e+09
`
	err := testutil.CollectAndCompare(source, strings.NewReader(expected),
		"hetzner_cloud_server_backup_enabled",
		"hetzner_cloud_server_cpu_cores",
		"hetzner_cloud_server_delete_protection",
		"hetzner_cloud_server_disk_bytes",
		"hetzner_cloud_server_locked",
		"hetzner_cloud_server_memory_bytes",
		"hetzner_cloud_server_type_deprecated",
		"hetzner_cloud_server_type_unavailable_after_timestamp_seconds",
	)
	if err != nil {
		t.Error(err)
	}
}

func TestCollectPrices(t *testing.T) {
	_, client := hcloudtest.NewServer(t, map[string]string{
		"/servers": `{"servers": [
			{"id": 30, "name": "app-1", "status": "off", "location": {"name": "fsn1"}, "backup_window": "22-02",
			 "created": "2026-01-01T00:00:00Z",
			 "server_type": {"name": "cx22", "cores": 2, "memory": 4.0, "prices": [
				{"location": "nbg1", "price_hourly": {"net": "0.0100", "gross": "0.0119"}, "price_monthly": {"net": "6.0000", "gross": "7.14"},
				 "included_traffic": 21990232555520, "price_per_tb_traffic": {"net": "1.0000", "gross": "1.19"}},
				{"location": "fsn1", "price_hourly": {"net": "0.0080", "gross": "0.0095"}, "price_monthly": {"net": "5.0000", "gross": "5.95"},
				 "included_traffic": 21990232555520, "price_per_tb_traffic": {"net": "1.0000", "gross": "1.19"}}
			 ]}},
			{"id": 31, "name": "app-2", "status": "off", "location": {"name": "fsn1"}, "backup_window": null,
			 "created": "2026-01-01T00:00:00Z",
			 "server_type": {"name": "cx22", "cores": 2, "memory": 4.0, "prices": [
				{"location": "fsn1", "price_hourly": {"net": "0.0080", "gross": "0.0095"}, "price_monthly": {"net": "5.0000", "gross": "5.95"},
				 "included_traffic": 21990232555520, "price_per_tb_traffic": {"net": "1.0000", "gross": "1.19"}}
			 ]}}
		], ` + hcloudtest.Pagination + `}`,
	})
	source := New(client, 2, time.Minute, hcloudtest.Prices, nil)
	if err := source.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// The price of the server's location (fsn1) is used; backups cost 20 %
	// extra and only for the server with backups enabled.
	expected := `
# HELP hetzner_cloud_server_backup_monthly_price Net monthly price of the server's backups (only if enabled)
# TYPE hetzner_cloud_server_backup_monthly_price gauge
hetzner_cloud_server_backup_monthly_price{currency="EUR",server="app-1"} 1
# HELP hetzner_cloud_server_created_timestamp_seconds Unix timestamp when the server was created
# TYPE hetzner_cloud_server_created_timestamp_seconds gauge
hetzner_cloud_server_created_timestamp_seconds{server="app-1"} 1.7672256e+09
hetzner_cloud_server_created_timestamp_seconds{server="app-2"} 1.7672256e+09
# HELP hetzner_cloud_server_monthly_price Net monthly price of the server (capped monthly price, without backups)
# TYPE hetzner_cloud_server_monthly_price gauge
hetzner_cloud_server_monthly_price{currency="EUR",server="app-1"} 5
hetzner_cloud_server_monthly_price{currency="EUR",server="app-2"} 5
# HELP hetzner_cloud_server_traffic_price_per_tb Net price per TB of outbound traffic above the included traffic
# TYPE hetzner_cloud_server_traffic_price_per_tb gauge
hetzner_cloud_server_traffic_price_per_tb{currency="EUR",server="app-1"} 1
hetzner_cloud_server_traffic_price_per_tb{currency="EUR",server="app-2"} 1
`
	if err := testutil.CollectAndCompare(source, strings.NewReader(expected),
		"hetzner_cloud_server_backup_monthly_price", "hetzner_cloud_server_created_timestamp_seconds",
		"hetzner_cloud_server_monthly_price", "hetzner_cloud_server_traffic_price_per_tb"); err != nil {
		t.Error(err)
	}

	// Pricing on but not fetched yet: the currency is unknown, so no prices
	// (they would flip from an assumed EUR to the real currency later).
	notYet := New(client, 2, time.Minute, func() *hcloud.Pricing { return nil }, nil)
	if err := notYet.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if n := testutil.CollectAndCount(notYet, "hetzner_cloud_server_monthly_price"); n != 0 {
		t.Errorf("prices before the price list = %d series, want 0", n)
	}

	// Pricing collector off: prices from the server type, assumed EUR.
	off := New(client, 2, time.Minute, nil, nil)
	if err := off.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if n := testutil.CollectAndCount(off, "hetzner_cloud_server_monthly_price"); n != 2 {
		t.Errorf("prices with pricing off = %d series, want 2", n)
	}
}

func TestMetricsInterval(t *testing.T) {
	t.Run("kept between fetches", func(t *testing.T) {
		fake, client := hcloudtest.NewServer(t, responses)
		source := New(client, 2, 2*time.Minute, nil, nil)

		for range 3 { // polls right after each other, well within 2 minutes
			if err := source.Fetch(context.Background()); err != nil {
				t.Fatalf("Fetch: %v", err)
			}
		}
		if got := fake.RequestCount("/servers/10/metrics"); got != 1 {
			t.Errorf("metrics requests = %d, want 1 within the interval", got)
		}
		if got := fake.RequestCount("/servers"); got != 3 {
			t.Errorf("list requests = %d, want 3 (every poll)", got)
		}
		if got := testutil.CollectAndCount(source, "hetzner_cloud_server_cpu_usage_ratio"); got != 1 {
			t.Errorf("cpu series = %d, want 1 (last values kept)", got)
		}
	})

	t.Run("off", func(t *testing.T) {
		fake, client := hcloudtest.NewServer(t, responses)
		source := New(client, 2, 0, nil, nil)
		if err := source.Fetch(context.Background()); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if got := fake.RequestCount("/servers/10/metrics"); got != 0 {
			t.Errorf("metrics requests = %d, want 0 when off", got)
		}
		if got := testutil.CollectAndCount(source, "hetzner_cloud_server_cpu_usage_ratio"); got != 0 {
			t.Errorf("cpu series = %d, want 0 when off", got)
		}
		if got := testutil.CollectAndCount(source, "hetzner_cloud_server_info"); got != 2 {
			t.Errorf("info series = %d, want 2 (list data unaffected)", got)
		}
	})
}

func TestLastBackup(t *testing.T) {
	_, client := hcloudtest.NewServer(t, responses)
	backups := map[int64]time.Time{10: time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC), 99: time.Now()}
	source := New(client, 2, 0, nil, func() map[int64]time.Time { return backups })
	if err := source.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Only servers with a backup get the metric; backups of unknown servers are ignored.
	expected := `
# HELP hetzner_cloud_server_last_backup_timestamp_seconds Unix timestamp when the server's newest available backup was created (from the images collector)
# TYPE hetzner_cloud_server_last_backup_timestamp_seconds gauge
hetzner_cloud_server_last_backup_timestamp_seconds{server="web-1"} 1.79082e+09
`
	if err := testutil.CollectAndCompare(source, strings.NewReader(expected), "hetzner_cloud_server_last_backup_timestamp_seconds"); err != nil {
		t.Error(err)
	}

	// Without the images collector there is no metric.
	without := New(client, 2, 0, nil, nil)
	if err := without.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := testutil.CollectAndCount(without, "hetzner_cloud_server_last_backup_timestamp_seconds"); got != 0 {
		t.Errorf("series without lookup = %d, want 0", got)
	}
}
