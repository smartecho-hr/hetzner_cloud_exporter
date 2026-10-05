package loadbalancer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/hetznercloud/hcloud-go/v2/hcloud/schema"
)

// The API returns target fields hcloud-go doesn't parse yet: the IP of a
// server target (a server can be a target once per IP, e.g. IPv4 and IPv6)
// and why a health check fails (health_status.detail, http_status_code,
// added 2026-08-17). listLoadBalancers reads the raw response, so these
// fields are available without extra API requests.

type rawTarget struct {
	Type   string `json:"type"`
	Server *struct {
		ID int64  `json:"id"`
		IP string `json:"ip"`
	} `json:"server"`
	IP *struct {
		IP string `json:"ip"`
	} `json:"ip"`
	HealthStatus []rawHealthStatus `json:"health_status"`
	Targets      []rawTarget       `json:"targets"` // servers matched by a label selector
}

type rawHealthStatus struct {
	ListenPort     int    `json:"listen_port"`
	Status         string `json:"status"`
	Detail         string `json:"detail"`
	HTTPStatusCode int    `json:"http_status_code"`
}

type listResponse struct {
	LoadBalancers []json.RawMessage `json:"load_balancers"`
	Meta          schema.Meta       `json:"meta"`
}

// listedLoadBalancer is a Load Balancer as parsed by hcloud-go plus the raw targets.
type listedLoadBalancer struct {
	loadBalancer *hcloud.LoadBalancer
	targets      []rawTarget
}

// listLoadBalancers lists all Load Balancers, page by page like hcloud-go's All().
func listLoadBalancers(ctx context.Context, client *hcloud.Client) ([]listedLoadBalancer, error) {
	var result []listedLoadBalancer

	// maxPages guards against a broken next_page that never ends
	// (50 per page: 50,000 Load Balancers).
	const maxPages = 1000
	for page := 1; page > 0; {
		if page > maxPages {
			return nil, fmt.Errorf("more than %d pages, giving up", maxPages)
		}
		req, err := client.NewRequest(ctx, http.MethodGet, fmt.Sprintf("/load_balancers?page=%d&per_page=50", page), nil)
		if err != nil {
			return nil, err
		}
		var body listResponse
		if _, err := client.Do(req, &body); err != nil {
			return nil, err
		}

		for _, raw := range body.LoadBalancers {
			listed, err := parseLoadBalancer(raw)
			if err != nil {
				return nil, err
			}
			result = append(result, listed)
		}

		next := 0
		if p := body.Meta.Pagination; p != nil {
			next = p.NextPage
		}
		if next != 0 && next <= page {
			return nil, fmt.Errorf("next page %d after page %d", next, page)
		}
		page = next
	}
	return result, nil
}

func parseLoadBalancer(raw json.RawMessage) (listedLoadBalancer, error) {
	var parsed schema.LoadBalancer
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return listedLoadBalancer{}, fmt.Errorf("parsing load balancer: %w", err)
	}
	var extra struct {
		Targets []rawTarget `json:"targets"`
	}
	if err := json.Unmarshal(raw, &extra); err != nil {
		return listedLoadBalancer{}, fmt.Errorf("parsing load balancer targets: %w", err)
	}
	return listedLoadBalancer{
		loadBalancer: hcloud.LoadBalancerFromSchema(parsed),
		targets:      extra.Targets,
	}, nil
}
