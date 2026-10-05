package sources

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/buildinfo"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcloudtest"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/poller"
)

// All sources and the exporter's own metrics must fit on one registry: no
// duplicate metric names, consistent help texts and labels. The pedantic
// registry also checks that every collected metric was described.
func TestAllSourcesOnOneRegistry(t *testing.T) {
	_, client := hcloudtest.NewServer(t, map[string]string{
		"/servers": `{"servers": [{"id": 10, "name": "web-1", "status": "running",
			"server_type": {"name": "cx22", "cores": 2}, "location": {"name": "fsn1"}}], ` + hcloudtest.Pagination + `}`,
		"/servers/10/metrics": `{"metrics": {"start": "2026-01-01T00:00:00Z", "end": "2026-01-01T00:01:00Z", "step": 60,
			"time_series": {"cpu": {"values": [[1767225660, "1"]]}, "disk.0.iops.read": {"values": [[1767225660, "2"]]}}}}`,
		"/load_balancers": `{"load_balancers": [{"id": 1, "name": "lb-1", "load_balancer_type": {"name": "lb11"},
			"services": [{"protocol": "tcp", "listen_port": 80}],
			"targets": [{"type": "server", "server": {"id": 10}, "health_status": [{"listen_port": 80, "status": "healthy"}]}]}],
			` + hcloudtest.Pagination + `}`,
		"/load_balancers/1/metrics": `{"metrics": {"start": "2026-01-01T00:00:00Z", "end": "2026-01-01T00:01:00Z", "step": 60,
			"time_series": {"open_connections": {"values": [[1767225660, "3"]]}}}}`,
		"/certificates": `{"certificates": [{"id": 1, "name": "c", "type": "managed", "not_valid_after": "2027-01-01T00:00:00Z",
			"status": {"issuance": "completed", "renewal": "scheduled"}}], ` + hcloudtest.Pagination + `}`,
		"/pricing": `{"pricing": {"currency": "EUR", "vat_rate": "19.00",
			"image": {"price_per_gb_month": {"net": "0.011", "gross": "0.013"}},
			"volume": {"price_per_gb_month": {"net": "0.044", "gross": "0.052"}},
			"server_backup": {"percentage": "20.00"}, "primary_ips": [], "floating_ips": [],
			"server_types": [], "load_balancer_types": [], "traffic": {"price_per_tb": {"net": "1", "gross": "1"}}}}`,
		"/volumes": `{"volumes": [{"id": 1, "name": "v", "status": "available", "size": 10, "server": null,
			"location": {"name": "fsn1"}, "protection": {"delete": false}, "created": "2026-01-01T00:00:00Z"}], ` + hcloudtest.Pagination + `}`,
		"/floating_ips": `{"floating_ips": [{"id": 1, "name": "f", "ip": "203.0.113.1", "type": "ipv4", "server": null,
			"home_location": {"name": "fsn1"}, "protection": {"delete": false}, "created": "2026-01-01T00:00:00Z", "dns_ptr": []}],
			` + hcloudtest.Pagination + `}`,
		"/primary_ips": `{"primary_ips": [{"id": 1, "name": "p", "ip": "203.0.113.2", "type": "ipv4", "assignee_id": 10,
			"assignee_type": "server", "location": {"name": "fsn1"}, "protection": {"delete": false}, "created": "2026-01-01T00:00:00Z"}],
			` + hcloudtest.Pagination + `}`,
		"/images": `{"images": [{"id": 1, "type": "snapshot", "status": "available", "description": "s", "image_size": 1.0,
			"disk_size": 20, "created": "2026-01-01T00:00:00Z", "created_from": {"id": 10, "name": "web-1"}, "protection": {"delete": false}},
			{"id": 2, "type": "backup", "status": "available", "image_size": 1.0, "disk_size": 20, "created": "2026-01-02T00:00:00Z",
			 "bound_to": 10, "protection": {"delete": false}}],
			` + hcloudtest.Pagination + `}`,
		"/storage_boxes": `{"storage_boxes": [{"id": 1, "name": "b", "username": "u1", "status": "active",
			"storage_box_type": {"name": "bx11", "size": 1000, "prices": []}, "location": {"name": "fsn1"},
			"access_settings": {}, "stats": {"size": 1, "size_data": 1, "size_snapshots": 0},
			"protection": {"delete": false}, "created": "2026-01-01T00:00:00Z", "labels": {}}], ` + hcloudtest.Pagination + `}`,
		"/ssh_keys": `{"ssh_keys": [{"id": 1, "name": "k", "fingerprint": "aa:bb", "public_key": "ssh-ed25519 AAAA",
			"created": "2026-01-01T00:00:00Z"}], ` + hcloudtest.Pagination + `}`,
	})

	all := Sources(client, Config{Concurrency: 2, MetricsInterval: time.Minute})
	registry := prometheus.NewPedanticRegistry()
	buildinfo.Register(registry)
	poller.New(all, poller.NewAPIStats(5), time.Minute).Register(registry)

	for _, source := range all {
		if err := source.Fetch(context.Background()); err != nil {
			t.Fatalf("%s: Fetch: %v", source.Name(), err)
		}
	}

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	names := map[string]bool{}
	for _, family := range families {
		names[family.GetName()] = true
	}
	for _, want := range []string{
		"hetzner_cloud_server_cpu_usage_ratio",
		"hetzner_cloud_server_last_backup_timestamp_seconds", // servers + images
		"hetzner_cloud_load_balancer_target_health_status",
		"hetzner_cloud_certificate_status",
		"hetzner_cloud_exporter_build_info",
		"hetzner_cloud_price_volume_per_gb_monthly",
		"hetzner_cloud_volume_monthly_price",
		"hetzner_cloud_floating_ip_assigned",
		"hetzner_cloud_primary_ip_info",
		"hetzner_cloud_image_monthly_price",
		"hetzner_cloud_storage_box_used_bytes",
		"hetzner_cloud_ssh_key_info",
	} {
		if !names[want] {
			t.Errorf("metric %s missing", want)
		}
	}
}

func TestSourcesRespectsEnabled(t *testing.T) {
	_, client := hcloudtest.NewServer(t, map[string]string{})

	names := func(enabled map[string]bool) []string {
		var result []string
		for _, s := range Sources(client, Config{Concurrency: 1, MetricsInterval: time.Minute, Enabled: enabled}) {
			result = append(result, s.Name())
		}
		return result
	}

	if got := names(nil); len(got) != len(Collectors) {
		t.Errorf("defaults: %v, want all %d collectors", got, len(Collectors))
	}
	onlyLB := map[string]bool{}
	for _, c := range Collectors {
		onlyLB[c.Name] = c.Name == "load_balancers"
	}
	got := names(onlyLB)
	if len(got) != 1 || got[0] != "load_balancers" {
		t.Errorf("only load_balancers enabled: got %v", got)
	}

	// Every collector name matches its source's Name(), which is the
	// "source" label of the exporter metrics.
	for _, s := range Sources(client, Config{Concurrency: 1, MetricsInterval: time.Minute}) {
		found := false
		for _, c := range Collectors {
			found = found || c.Name == s.Name()
		}
		if !found {
			t.Errorf("source %q has no entry in Collectors", s.Name())
		}
	}
}

func TestSourcesIntervals(t *testing.T) {
	_, client := hcloudtest.NewServer(t, map[string]string{})
	intervals := func(cfg Config) map[string]time.Duration {
		result := map[string]time.Duration{}
		for _, s := range Sources(client, cfg) {
			result[s.Name()] = s.(poller.Throttled).MinInterval()
		}
		return result
	}

	// Defaults from Collectors; collectors without one are fetched every poll.
	got := intervals(Config{Concurrency: 1})
	for name, want := range map[string]time.Duration{"pricing": time.Hour, "images": 10 * time.Minute,
		"storage_boxes": 5 * time.Minute, "ssh_keys": time.Hour, "servers": 0, "volumes": 0} {
		if got[name] != want {
			t.Errorf("default interval %s = %v, want %v", name, got[name], want)
		}
	}

	// Configured intervals override the defaults, 0 included.
	got = intervals(Config{Concurrency: 1, Intervals: map[string]time.Duration{"pricing": 0, "volumes": time.Hour}})
	if got["pricing"] != 0 || got["volumes"] != time.Hour || got["images"] != 10*time.Minute {
		t.Errorf("configured intervals = %v", got)
	}
}
