package primaryip

import (
	"context"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcloudtest"
)

func TestCollect(t *testing.T) {
	_, client := hcloudtest.NewServer(t, map[string]string{
		"/primary_ips": `{"primary_ips": [
			{"id": 1, "name": "web-v4", "ip": "203.0.113.1", "type": "ipv4", "assignee_id": 10, "assignee_type": "server",
			 "auto_delete": true, "blocked": false, "location": {"name": "fsn1"}, "protection": {"delete": false}, "created": "2026-01-01T00:00:00Z"},
			{"id": 2, "name": "spare-v4", "ip": "203.0.113.2", "type": "ipv4", "assignee_id": null, "assignee_type": "server",
			 "auto_delete": false, "blocked": false, "location": {"name": "fsn1"}, "protection": {"delete": true}, "created": "2026-01-01T00:00:00Z"},
			{"id": 3, "name": "web-v6", "ip": "2001:db8::", "type": "ipv6", "assignee_id": 10, "assignee_type": "server",
			 "auto_delete": true, "blocked": false, "location": {"name": "fsn1"}, "protection": {"delete": false}, "created": "2026-01-01T00:00:00Z"}
		], ` + hcloudtest.Pagination + `}`,
	})
	source := New(client, hcloudtest.Prices)
	if err := source.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// IPv6 has no price, the unassigned IPv4 still costs money.
	expected := `
# HELP hetzner_cloud_primary_ip_assigned Whether the primary IP is assigned to a server (1=assigned); unassigned IPv4 still costs money
# TYPE hetzner_cloud_primary_ip_assigned gauge
hetzner_cloud_primary_ip_assigned{primary_ip="spare-v4"} 0
hetzner_cloud_primary_ip_assigned{primary_ip="web-v4"} 1
hetzner_cloud_primary_ip_assigned{primary_ip="web-v6"} 1
# HELP hetzner_cloud_primary_ip_monthly_price Net monthly price of the primary IP (IPv6 is free)
# TYPE hetzner_cloud_primary_ip_monthly_price gauge
hetzner_cloud_primary_ip_monthly_price{currency="EUR",primary_ip="spare-v4"} 0.5
hetzner_cloud_primary_ip_monthly_price{currency="EUR",primary_ip="web-v4"} 0.5
`
	if err := testutil.CollectAndCompare(source, strings.NewReader(expected),
		"hetzner_cloud_primary_ip_assigned", "hetzner_cloud_primary_ip_monthly_price"); err != nil {
		t.Error(err)
	}
	if problems, err := testutil.CollectAndLint(source); err != nil || len(problems) > 0 {
		t.Errorf("lint: err=%v problems=%v", err, problems)
	}
}
