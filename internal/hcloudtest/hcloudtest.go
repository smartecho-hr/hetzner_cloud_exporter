// Package hcloudtest provides a fake Hetzner Cloud API for tests.
package hcloudtest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

// Pagination is the "meta" object list endpoints return for a single page.
const Pagination = `"meta": {"pagination": {"page": 1, "per_page": 50, "next_page": null, "last_page": 1, "total_entries": 1}}`

// Server is a fake Hetzner API. Responses maps a URL path to a JSON body; a
// key with a query ("/load_balancers?page=2&per_page=50") matches that exact
// request first. Paths not in the map return 404, paths marked with Fail
// return 500. Responses must not be changed after the server started.
type Server struct {
	*httptest.Server
	Responses map[string]string

	mu       sync.Mutex
	failing  map[string]bool
	status   map[string]int
	requests map[string]int
	queries  map[string][]string
}

// Queries returns the raw query strings of the requests made to path.
func (f *Server) Queries(path string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.queries[path]...)
}

// Recover makes a path answer normally again after Fail or Status.
func (f *Server) Recover(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.failing, path)
	delete(f.status, path)
}

// Status makes all following requests to path answer with an HTTP status
// and the matching Hetzner error code (401 unauthorized, 403 forbidden,
// 429 rate_limit_exceeded, with RateLimit headers).
func (f *Server) Status(path string, code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[path] = code
}

// Fail makes all following requests to path return an error.
func (f *Server) Fail(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failing[path] = true
}

// RequestCount returns how many requests were made to path.
func (f *Server) RequestCount(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[path]
}

// NewServer starts a fake API and returns it with a client pointing at it.
func NewServer(t *testing.T, responses map[string]string) (*Server, *hcloud.Client) {
	t.Helper()
	fake := &Server{Responses: responses, failing: map[string]bool{}, status: map[string]int{},
		requests: map[string]int{}, queries: map[string][]string{}}
	fake.Server = httptest.NewServer(http.HandlerFunc(fake.handle))
	t.Cleanup(fake.Close)

	client := hcloud.NewClient(
		hcloud.WithEndpoint(fake.URL),
		hcloud.WithHetznerEndpoint(fake.URL), // Storage Boxes
		hcloud.WithToken("test"),
		hcloud.WithRetryOpts(hcloud.RetryOpts{MaxRetries: 0}),
	)
	return fake, client
}

func (f *Server) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests[r.URL.Path]++
	f.queries[r.URL.Path] = append(f.queries[r.URL.Path], r.URL.RawQuery)
	failing := f.failing[r.URL.Path]
	status := f.status[r.URL.Path]
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("RateLimit-Limit", "3600")
	w.Header().Set("RateLimit-Remaining", "3500")

	if status != 0 {
		code := map[int]string{
			http.StatusUnauthorized:    "unauthorized",
			http.StatusForbidden:       "forbidden",
			http.StatusTooManyRequests: "rate_limit_exceeded",
		}[status]
		if status == http.StatusTooManyRequests {
			w.Header().Set("RateLimit-Remaining", "0")
		}
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"error": {"code": %q, "message": "test"}}`, code)
		return
	}

	body, ok := f.Responses[r.URL.Path+"?"+r.URL.RawQuery]
	if !ok {
		body, ok = f.Responses[r.URL.Path]
	}
	switch {
	case failing:
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": {"code": "server_error", "message": "boom"}}`))
	case !ok:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error": {"code": "not_found", "message": "not found"}}`))
	default:
		_, _ = w.Write([]byte(body))
	}
}

// Prices returns a price list for tests: volumes 0.044/GB, snapshots
// 0.011/GB, backups 20 %, primary IPv4 0.50 and floating IPv4 3.00 in fsn1.
func Prices() *hcloud.Pricing {
	return &hcloud.Pricing{
		Currency:     "EUR",
		Volume:       hcloud.VolumePricing{PerGBMonthly: hcloud.Price{Net: "0.0440"}},
		Image:        hcloud.ImagePricing{PerGBMonth: hcloud.Price{Net: "0.0110"}},
		ServerBackup: hcloud.ServerBackupPricing{Percentage: "20.00"},
		PrimaryIPs: []hcloud.PrimaryIPPricing{{Type: "ipv4", Pricings: []hcloud.PrimaryIPTypePricing{
			{Location: "fsn1", Monthly: hcloud.PrimaryIPPrice{Net: "0.5000"}},
		}}},
		FloatingIPs: []hcloud.FloatingIPTypePricing{{Type: hcloud.FloatingIPTypeIPv4, Pricings: []hcloud.FloatingIPTypeLocationPricing{
			{Location: &hcloud.Location{Name: "fsn1"}, Monthly: hcloud.Price{Net: "3.0000"}},
		}}},
	}
}
