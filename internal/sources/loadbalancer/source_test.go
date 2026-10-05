package loadbalancer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcloudtest"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcmetrics"
)

// lb-1 (HTTP) has:
//   - server 10 as a direct IPv4 target and again with an IPv6 address
//     (dual stack, must stay two targets),
//   - a label selector matching server 10 (duplicate of the direct IPv4
//     target, must be dropped) and server 11 (unknown status),
//   - an IP target that fails its HTTP check with a reason code.
//
// lb-2 is TCP-only without targets and without requests_per_second data.
// lb-3 is HTTP without requests_per_second data (must stay a gap, not 0).
var responses = map[string]string{
	"/load_balancers": `{"load_balancers": [
		{"id": 1, "name": "lb-1", "location": {"name": "fsn1"},
		 "load_balancer_type": {"name": "lb11", "max_connections": 10000, "max_services": 5, "max_targets": 25, "max_assigned_certificates": 10},
		 "outgoing_traffic": 100, "ingoing_traffic": 200, "included_traffic": 1000,
		 "services": [
			{"protocol": "https", "listen_port": 443, "http": {"certificates": [1, 2]}},
			{"protocol": "https", "listen_port": 8443, "http": {"certificates": [1]}}
		 ],
		 "targets": [
			{"type": "server", "server": {"id": 10},
			 "health_status": [{"listen_port": 443, "status": "healthy"}, {"listen_port": 8443, "status": "unhealthy", "detail": "layer4_timeout"}]},
			{"type": "server", "server": {"id": 10, "ip": "2001:db8::1"},
			 "health_status": [{"listen_port": 443, "status": "unhealthy", "detail": "layer4_no_connection"}]},
			{"type": "label_selector", "label_selector": {"selector": "role=web"}, "targets": [
				{"type": "server", "server": {"id": 10},
				 "health_status": [{"listen_port": 443, "status": "healthy"}, {"listen_port": 8443, "status": "unhealthy", "detail": "layer4_timeout"}]},
				{"type": "server", "server": {"id": 11},
				 "health_status": [{"listen_port": 443, "status": "unknown"}]}
			]},
			{"type": "ip", "ip": {"ip": "203.0.113.5"},
			 "health_status": [{"listen_port": 443, "status": "unhealthy", "detail": "unexpected_http_status", "http_status_code": 502}]}
		 ]},
		{"id": 2, "name": "lb-2", "load_balancer_type": {"name": "lb11"}, "location": {"name": "nbg1"},
		 "services": [{"protocol": "tcp", "listen_port": 5432}], "targets": []},
		{"id": 3, "name": "lb-3", "load_balancer_type": {"name": "lb11"}, "location": {"name": "nbg1"},
		 "services": [{"protocol": "http", "listen_port": 80}], "targets": []}
	], ` + hcloudtest.Pagination + `}`,
	"/load_balancers/1/metrics": `{"metrics": {"start": "2026-01-01T00:00:00Z", "end": "2026-01-01T00:01:00Z", "step": 60, "time_series": {
		"open_connections": {"values": [[1767225600, "1"], [1767225660, "42"]]},
		"connections_per_second": {"values": [[1767225660, "3.5"]]},
		"requests_per_second": {"values": [[1767225600, "12"], [1767225660, "NaN"]]},
		"bandwidth.in": {"values": [[1767225660, "1000"]]},
		"bandwidth.out": {"values": [[1767225660, "2000"]]}
	}}}`,
	"/load_balancers/2/metrics": `{"metrics": {"start": "2026-01-01T00:00:00Z", "end": "2026-01-01T00:01:00Z", "step": 60, "time_series": {
		"open_connections": {"values": [[1767225660, "7"]]}
	}}}`,
	"/load_balancers/3/metrics": `{"metrics": {"start": "2026-01-01T00:00:00Z", "end": "2026-01-01T00:01:00Z", "step": 60, "time_series": {}}}`,
}

var serverNames = map[int64]string{10: "web-1", 11: "web-2"}

func serverName(id int64) string { return serverNames[id] }

func newSource(t *testing.T) (*hcloudtest.Server, *Source) {
	t.Helper()
	fake, client := hcloudtest.NewServer(t, responses)
	return fake, New(client, 2, time.Minute, serverName, nil)
}

func TestCollect(t *testing.T) {
	_, source := newSource(t)
	if err := source.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	expected := `
# HELP hetzner_cloud_load_balancer_bandwidth_bytes_per_second Bandwidth of the Load Balancer (bytes/s)
# TYPE hetzner_cloud_load_balancer_bandwidth_bytes_per_second gauge
hetzner_cloud_load_balancer_bandwidth_bytes_per_second{direction="in",load_balancer="lb-1"} 1000
hetzner_cloud_load_balancer_bandwidth_bytes_per_second{direction="out",load_balancer="lb-1"} 2000
# HELP hetzner_cloud_load_balancer_info Load Balancer information, always 1
# TYPE hetzner_cloud_load_balancer_info gauge
hetzner_cloud_load_balancer_info{load_balancer="lb-1",load_balancer_id="1",load_balancer_type="lb11",location="fsn1"} 1
hetzner_cloud_load_balancer_info{load_balancer="lb-2",load_balancer_id="2",load_balancer_type="lb11",location="nbg1"} 1
hetzner_cloud_load_balancer_info{load_balancer="lb-3",load_balancer_id="3",load_balancer_type="lb11",location="nbg1"} 1
# HELP hetzner_cloud_load_balancer_open_connections Current open connections on the Load Balancer
# TYPE hetzner_cloud_load_balancer_open_connections gauge
hetzner_cloud_load_balancer_open_connections{load_balancer="lb-1"} 42
hetzner_cloud_load_balancer_open_connections{load_balancer="lb-2"} 7
# HELP hetzner_cloud_load_balancer_requests_per_second HTTP requests per second on the Load Balancer (0 for TCP-only Load Balancers)
# TYPE hetzner_cloud_load_balancer_requests_per_second gauge
hetzner_cloud_load_balancer_requests_per_second{load_balancer="lb-1"} 12
hetzner_cloud_load_balancer_requests_per_second{load_balancer="lb-2"} 0
# HELP hetzner_cloud_load_balancer_target_healthy Health of a target for one Load Balancer service (1=healthy, 0=unhealthy or unknown)
# TYPE hetzner_cloud_load_balancer_target_healthy gauge
hetzner_cloud_load_balancer_target_healthy{ip="",listen_port="443",load_balancer="lb-1",server="web-1",server_id="10",target_type="server"} 1
hetzner_cloud_load_balancer_target_healthy{ip="",listen_port="8443",load_balancer="lb-1",server="web-1",server_id="10",target_type="server"} 0
hetzner_cloud_load_balancer_target_healthy{ip="2001:db8::1",listen_port="443",load_balancer="lb-1",server="web-1",server_id="10",target_type="server"} 0
hetzner_cloud_load_balancer_target_healthy{ip="",listen_port="443",load_balancer="lb-1",server="web-2",server_id="11",target_type="server"} 0
hetzner_cloud_load_balancer_target_healthy{ip="203.0.113.5",listen_port="443",load_balancer="lb-1",server="",server_id="",target_type="ip"} 0
# HELP hetzner_cloud_load_balancer_target_health_status Health check result of a target for one service, always 1. detail and http_status_code explain why a target is unhealthy
# TYPE hetzner_cloud_load_balancer_target_health_status gauge
hetzner_cloud_load_balancer_target_health_status{detail="",http_status_code="",ip="",listen_port="443",load_balancer="lb-1",server="web-1",server_id="10",status="healthy",target_type="server"} 1
hetzner_cloud_load_balancer_target_health_status{detail="layer4_timeout",http_status_code="",ip="",listen_port="8443",load_balancer="lb-1",server="web-1",server_id="10",status="unhealthy",target_type="server"} 1
hetzner_cloud_load_balancer_target_health_status{detail="layer4_no_connection",http_status_code="",ip="2001:db8::1",listen_port="443",load_balancer="lb-1",server="web-1",server_id="10",status="unhealthy",target_type="server"} 1
hetzner_cloud_load_balancer_target_health_status{detail="",http_status_code="",ip="",listen_port="443",load_balancer="lb-1",server="web-2",server_id="11",status="unknown",target_type="server"} 1
hetzner_cloud_load_balancer_target_health_status{detail="unexpected_http_status",http_status_code="502",ip="203.0.113.5",listen_port="443",load_balancer="lb-1",server="",server_id="",status="unhealthy",target_type="ip"} 1
# HELP hetzner_cloud_load_balancer_target_unhealthy_services Number of Load Balancer services for which the target is unhealthy (unknown is not counted)
# TYPE hetzner_cloud_load_balancer_target_unhealthy_services gauge
hetzner_cloud_load_balancer_target_unhealthy_services{ip="",load_balancer="lb-1",server="web-1",server_id="10",target_type="server"} 1
hetzner_cloud_load_balancer_target_unhealthy_services{ip="2001:db8::1",load_balancer="lb-1",server="web-1",server_id="10",target_type="server"} 1
hetzner_cloud_load_balancer_target_unhealthy_services{ip="",load_balancer="lb-1",server="web-2",server_id="11",target_type="server"} 0
hetzner_cloud_load_balancer_target_unhealthy_services{ip="203.0.113.5",load_balancer="lb-1",server="",server_id="",target_type="ip"} 1
# HELP hetzner_cloud_load_balancer_targets Number of targets, with label selectors expanded into the servers they match
# TYPE hetzner_cloud_load_balancer_targets gauge
hetzner_cloud_load_balancer_targets{load_balancer="lb-1"} 4
hetzner_cloud_load_balancer_targets{load_balancer="lb-2"} 0
hetzner_cloud_load_balancer_targets{load_balancer="lb-3"} 0
`
	err := testutil.CollectAndCompare(source, strings.NewReader(expected),
		"hetzner_cloud_load_balancer_bandwidth_bytes_per_second",
		"hetzner_cloud_load_balancer_info",
		"hetzner_cloud_load_balancer_open_connections",
		"hetzner_cloud_load_balancer_requests_per_second",
		"hetzner_cloud_load_balancer_target_healthy",
		"hetzner_cloud_load_balancer_target_health_status",
		"hetzner_cloud_load_balancer_target_unhealthy_services",
		"hetzner_cloud_load_balancer_targets",
	)
	if err != nil {
		t.Error(err)
	}

	// The whole output must be valid, e.g. no duplicate series, and every
	// collected metric must have been described.
	if problems, err := testutil.CollectAndLint(source); err != nil || len(problems) > 0 {
		t.Errorf("lint: err=%v problems=%v", err, problems)
	}
}

func TestCollectLimits(t *testing.T) {
	_, source := newSource(t)
	if err := source.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	expected := `
# HELP hetzner_cloud_load_balancer_certificates Number of distinct certificates assigned to services
# TYPE hetzner_cloud_load_balancer_certificates gauge
hetzner_cloud_load_balancer_certificates{load_balancer="lb-1"} 2
hetzner_cloud_load_balancer_certificates{load_balancer="lb-2"} 0
hetzner_cloud_load_balancer_certificates{load_balancer="lb-3"} 0
# HELP hetzner_cloud_load_balancer_max_certificates Maximum number of assigned certificates of the Load Balancer type
# TYPE hetzner_cloud_load_balancer_max_certificates gauge
hetzner_cloud_load_balancer_max_certificates{load_balancer="lb-1"} 10
hetzner_cloud_load_balancer_max_certificates{load_balancer="lb-2"} 0
hetzner_cloud_load_balancer_max_certificates{load_balancer="lb-3"} 0
# HELP hetzner_cloud_load_balancer_max_connections Maximum concurrent connections of the Load Balancer type
# TYPE hetzner_cloud_load_balancer_max_connections gauge
hetzner_cloud_load_balancer_max_connections{load_balancer="lb-1"} 10000
hetzner_cloud_load_balancer_max_connections{load_balancer="lb-2"} 0
hetzner_cloud_load_balancer_max_connections{load_balancer="lb-3"} 0
# HELP hetzner_cloud_load_balancer_max_services Maximum number of services of the Load Balancer type
# TYPE hetzner_cloud_load_balancer_max_services gauge
hetzner_cloud_load_balancer_max_services{load_balancer="lb-1"} 5
hetzner_cloud_load_balancer_max_services{load_balancer="lb-2"} 0
hetzner_cloud_load_balancer_max_services{load_balancer="lb-3"} 0
# HELP hetzner_cloud_load_balancer_max_targets Maximum number of targets of the Load Balancer type
# TYPE hetzner_cloud_load_balancer_max_targets gauge
hetzner_cloud_load_balancer_max_targets{load_balancer="lb-1"} 25
hetzner_cloud_load_balancer_max_targets{load_balancer="lb-2"} 0
hetzner_cloud_load_balancer_max_targets{load_balancer="lb-3"} 0
# HELP hetzner_cloud_load_balancer_services Number of configured services
# TYPE hetzner_cloud_load_balancer_services gauge
hetzner_cloud_load_balancer_services{load_balancer="lb-1"} 2
hetzner_cloud_load_balancer_services{load_balancer="lb-2"} 1
hetzner_cloud_load_balancer_services{load_balancer="lb-3"} 1
`
	err := testutil.CollectAndCompare(source, strings.NewReader(expected),
		"hetzner_cloud_load_balancer_certificates",
		"hetzner_cloud_load_balancer_max_certificates",
		"hetzner_cloud_load_balancer_max_connections",
		"hetzner_cloud_load_balancer_max_services",
		"hetzner_cloud_load_balancer_max_targets",
		"hetzner_cloud_load_balancer_services",
	)
	if err != nil {
		t.Error(err)
	}
}

func TestFetchKeepsLoadBalancersWhenMetricsFail(t *testing.T) {
	fake, source := newSource(t)
	fake.Fail("/load_balancers/2/metrics")

	// A failed metrics request is a partial error: the list is fresh.
	if err := source.Fetch(context.Background()); !hcmetrics.IsPartial(err) {
		t.Fatalf("err = %v, want a partial error", err)
	}

	// lb-2 still reports info, only its metrics are missing.
	if got := testutil.CollectAndCount(source, "hetzner_cloud_load_balancer_info"); got != 3 {
		t.Errorf("info series = %d, want 3", got)
	}
	if got := testutil.CollectAndCount(source, "hetzner_cloud_load_balancer_open_connections"); got != 1 {
		t.Errorf("open_connections series = %d, want 1", got)
	}
}

func TestFetchKeepsPreviousDataWhenListFails(t *testing.T) {
	fake, source := newSource(t)
	if err := source.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	fake.Fail("/load_balancers")
	if err := source.Fetch(context.Background()); err == nil {
		t.Fatal("expected error for failed list request")
	}
	if got := testutil.CollectAndCount(source, "hetzner_cloud_load_balancer_info"); got != 3 {
		t.Errorf("info series after failed list = %d, want previous 3", got)
	}

	// The API recovers: the next poll succeeds again.
	fake.Recover("/load_balancers")
	if err := source.Fetch(context.Background()); err != nil {
		t.Errorf("after recovery: %v", err)
	}
}

func TestListFollowsPagination(t *testing.T) {
	_, client := hcloudtest.NewServer(t, map[string]string{
		"/load_balancers?page=1&per_page=50": `{"load_balancers": [{"id": 1, "name": "lb-1", "targets": []}],
			"meta": {"pagination": {"page": 1, "per_page": 50, "next_page": 2, "last_page": 2, "total_entries": 2}}}`,
		"/load_balancers?page=2&per_page=50": `{"load_balancers": [{"id": 2, "name": "lb-2", "targets": []}],
			"meta": {"pagination": {"page": 2, "per_page": 50, "next_page": null, "last_page": 2, "total_entries": 2}}}`,
	})

	listed, err := listLoadBalancers(context.Background(), client)
	if err != nil {
		t.Fatalf("listLoadBalancers: %v", err)
	}
	if len(listed) != 2 || listed[0].loadBalancer.Name != "lb-1" || listed[1].loadBalancer.Name != "lb-2" {
		t.Errorf("listed %d load balancers, want lb-1 and lb-2", len(listed))
	}
}

func TestCollectServices(t *testing.T) {
	_, client := hcloudtest.NewServer(t, map[string]string{
		"/load_balancers": `{"load_balancers": [
			{"id": 1, "name": "lb-1", "protection": {"delete": true},
			 "load_balancer_type": {"name": "lb11", "deprecation": {"announced": "2026-06-01T00:00:00Z", "unavailable_after": "2026-12-01T00:00:00Z"}},
			 "services": [
				{"protocol": "https", "listen_port": 443, "destination_port": 8080, "proxyprotocol": false,
				 "health_check": {"protocol": "http", "port": 8080, "interval": 15, "timeout": 10, "retries": 3}},
				{"protocol": "tcp", "listen_port": 5432, "destination_port": 5432, "proxyprotocol": true,
				 "health_check": {"protocol": "tcp", "port": 5432, "interval": 10, "timeout": 5, "retries": 2}}
			 ], "targets": []}
		], ` + hcloudtest.Pagination + `}`,
		"/load_balancers/1/metrics": `{"metrics": {"start": "2026-01-01T00:00:00Z", "end": "2026-01-01T00:01:00Z", "step": 60, "time_series": {}}}`,
	})
	source := New(client, 2, time.Minute, nil, nil)
	if err := source.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	expected := `
# HELP hetzner_cloud_load_balancer_delete_protection Whether delete protection is enabled (1=enabled)
# TYPE hetzner_cloud_load_balancer_delete_protection gauge
hetzner_cloud_load_balancer_delete_protection{load_balancer="lb-1"} 1
# HELP hetzner_cloud_load_balancer_health_check_interval_seconds Interval of the service's health check
# TYPE hetzner_cloud_load_balancer_health_check_interval_seconds gauge
hetzner_cloud_load_balancer_health_check_interval_seconds{listen_port="443",load_balancer="lb-1"} 15
hetzner_cloud_load_balancer_health_check_interval_seconds{listen_port="5432",load_balancer="lb-1"} 10
# HELP hetzner_cloud_load_balancer_health_check_retries Failed health checks before a target is marked unhealthy
# TYPE hetzner_cloud_load_balancer_health_check_retries gauge
hetzner_cloud_load_balancer_health_check_retries{listen_port="443",load_balancer="lb-1"} 3
hetzner_cloud_load_balancer_health_check_retries{listen_port="5432",load_balancer="lb-1"} 2
# HELP hetzner_cloud_load_balancer_service_info Load Balancer service configuration, always 1
# TYPE hetzner_cloud_load_balancer_service_info gauge
hetzner_cloud_load_balancer_service_info{destination_port="5432",listen_port="5432",load_balancer="lb-1",protocol="tcp",proxyprotocol="true"} 1
hetzner_cloud_load_balancer_service_info{destination_port="8080",listen_port="443",load_balancer="lb-1",protocol="https",proxyprotocol="false"} 1
# HELP hetzner_cloud_load_balancer_type_deprecated Whether the Load Balancer type is deprecated (1=deprecated)
# TYPE hetzner_cloud_load_balancer_type_deprecated gauge
hetzner_cloud_load_balancer_type_deprecated{load_balancer="lb-1"} 1
# HELP hetzner_cloud_load_balancer_type_unavailable_after_timestamp_seconds Unix timestamp after which the deprecated Load Balancer type can no longer be ordered
# TYPE hetzner_cloud_load_balancer_type_unavailable_after_timestamp_seconds gauge
hetzner_cloud_load_balancer_type_unavailable_after_timestamp_seconds{load_balancer="lb-1"} 1.7960832e+09
`
	err := testutil.CollectAndCompare(source, strings.NewReader(expected),
		"hetzner_cloud_load_balancer_delete_protection",
		"hetzner_cloud_load_balancer_health_check_interval_seconds",
		"hetzner_cloud_load_balancer_health_check_retries",
		"hetzner_cloud_load_balancer_service_info",
		"hetzner_cloud_load_balancer_type_deprecated",
		"hetzner_cloud_load_balancer_type_unavailable_after_timestamp_seconds",
	)
	if err != nil {
		t.Error(err)
	}
	if problems, err := testutil.CollectAndLint(source); err != nil || len(problems) > 0 {
		t.Errorf("lint: err=%v problems=%v", err, problems)
	}
}

func TestCollectPrices(t *testing.T) {
	_, client := hcloudtest.NewServer(t, map[string]string{
		"/load_balancers": `{"load_balancers": [
			{"id": 1, "name": "lb-1", "location": {"name": "fsn1"}, "created": "2026-01-01T00:00:00Z",
			 "load_balancer_type": {"name": "lb11", "prices": [
				{"location": "fsn1", "price_hourly": {"net": "0.0090", "gross": "0.0107"}, "price_monthly": {"net": "5.3900", "gross": "6.41"},
				 "included_traffic": 21990232555520, "price_per_tb_traffic": {"net": "1.0000", "gross": "1.19"}}]},
			 "services": [], "targets": []}
		], ` + hcloudtest.Pagination + `}`,
		"/load_balancers/1/metrics": `{"metrics": {"start": "2026-01-01T00:00:00Z", "end": "2026-01-01T00:01:00Z", "step": 60, "time_series": {}}}`,
	})
	source := New(client, 2, time.Minute, nil, nil)
	if err := source.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	expected := `
# HELP hetzner_cloud_load_balancer_hourly_price Net hourly price of the Load Balancer
# TYPE hetzner_cloud_load_balancer_hourly_price gauge
hetzner_cloud_load_balancer_hourly_price{currency="EUR",load_balancer="lb-1"} 0.009
# HELP hetzner_cloud_load_balancer_monthly_price Net monthly price of the Load Balancer
# TYPE hetzner_cloud_load_balancer_monthly_price gauge
hetzner_cloud_load_balancer_monthly_price{currency="EUR",load_balancer="lb-1"} 5.39
# HELP hetzner_cloud_load_balancer_traffic_price_per_tb Net price per TB of outbound traffic above the included traffic
# TYPE hetzner_cloud_load_balancer_traffic_price_per_tb gauge
hetzner_cloud_load_balancer_traffic_price_per_tb{currency="EUR",load_balancer="lb-1"} 1
`
	if err := testutil.CollectAndCompare(source, strings.NewReader(expected),
		"hetzner_cloud_load_balancer_hourly_price", "hetzner_cloud_load_balancer_monthly_price", "hetzner_cloud_load_balancer_traffic_price_per_tb"); err != nil {
		t.Error(err)
	}
}

func TestMetricsOff(t *testing.T) {
	fake, client := hcloudtest.NewServer(t, responses)
	source := New(client, 2, 0, serverName, nil)
	if err := source.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := fake.RequestCount("/load_balancers/1/metrics"); got != 0 {
		t.Errorf("metrics requests = %d, want 0 when off", got)
	}
	// Target health comes from the list and stays available.
	if got := testutil.CollectAndCount(source, "hetzner_cloud_load_balancer_target_health_status"); got != 5 {
		t.Errorf("target health series = %d, want 5", got)
	}
	if got := testutil.CollectAndCount(source, "hetzner_cloud_load_balancer_open_connections"); got != 0 {
		t.Errorf("open_connections series = %d, want 0 when off", got)
	}
}
