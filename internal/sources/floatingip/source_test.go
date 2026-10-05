package floatingip

import (
	"context"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcloudtest"
)

func TestCollect(t *testing.T) {
	_, client := hcloudtest.NewServer(t, map[string]string{
		"/floating_ips": `{"floating_ips": [
			{"id": 1, "name": "vip", "ip": "203.0.113.10", "type": "ipv4", "server": null, "blocked": false,
			 "home_location": {"name": "fsn1"}, "protection": {"delete": false}, "created": "2026-01-01T00:00:00Z", "dns_ptr": []},
			{"id": 2, "name": "", "ip": "203.0.113.11", "type": "ipv4", "server": 10, "blocked": true,
			 "home_location": {"name": "fsn1"}, "protection": {"delete": true}, "created": "2026-01-01T00:00:00Z", "dns_ptr": []}
		], ` + hcloudtest.Pagination + `}`,
	})
	source := New(client, hcloudtest.Prices)
	if err := source.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// An IP without a name is labelled with its address.
	expected := `
# HELP hetzner_cloud_floating_ip_assigned Whether the floating IP is assigned to a server (1=assigned); unassigned IPs still cost money
# TYPE hetzner_cloud_floating_ip_assigned gauge
hetzner_cloud_floating_ip_assigned{floating_ip="203.0.113.11"} 1
hetzner_cloud_floating_ip_assigned{floating_ip="vip"} 0
# HELP hetzner_cloud_floating_ip_blocked Whether Hetzner blocked the IP, e.g. after abuse (1=blocked)
# TYPE hetzner_cloud_floating_ip_blocked gauge
hetzner_cloud_floating_ip_blocked{floating_ip="203.0.113.11"} 1
hetzner_cloud_floating_ip_blocked{floating_ip="vip"} 0
# HELP hetzner_cloud_floating_ip_monthly_price Net monthly price of the floating IP
# TYPE hetzner_cloud_floating_ip_monthly_price gauge
hetzner_cloud_floating_ip_monthly_price{currency="EUR",floating_ip="203.0.113.11"} 3
hetzner_cloud_floating_ip_monthly_price{currency="EUR",floating_ip="vip"} 3
`
	if err := testutil.CollectAndCompare(source, strings.NewReader(expected),
		"hetzner_cloud_floating_ip_assigned", "hetzner_cloud_floating_ip_blocked", "hetzner_cloud_floating_ip_monthly_price"); err != nil {
		t.Error(err)
	}
	if problems, err := testutil.CollectAndLint(source); err != nil || len(problems) > 0 {
		t.Errorf("lint: err=%v problems=%v", err, problems)
	}
}
