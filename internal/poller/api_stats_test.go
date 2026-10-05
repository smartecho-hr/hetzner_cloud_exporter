package poller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcmetrics"
)

func TestAPIStatsBlocksAfterRateLimit(t *testing.T) {
	var hits atomic.Int32
	status := http.StatusTooManyRequests
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
	}))
	defer srv.Close()

	stats := NewAPIStats(5)
	client := stats.HTTPClient()

	// The 429 counter exists before any 429, so alerts see the first one.
	if got := testutil.ToFloat64(stats.responses.WithLabelValues("429")); got != 0 {
		t.Fatalf("429 counter before any request = %v, want 0 (pre-created)", got)
	}

	stats.StartPoll()
	resp, err := client.Get(srv.URL)
	if err != nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("first request: %v %v", resp, err)
	}
	resp.Body.Close()

	// Further requests of the same poll are not sent.
	resp, err = client.Get(srv.URL)
	if err == nil {
		resp.Body.Close()
	}
	if !errors.Is(err, hcmetrics.ErrRateLimited) {
		t.Errorf("second request: err = %v, want ErrRateLimited", err)
	}
	if hits.Load() != 1 || stats.Requests() != 1 {
		t.Errorf("sent %d requests (counted %d), want 1", hits.Load(), stats.Requests())
	}

	// The next poll tries again.
	status = http.StatusOK
	stats.StartPoll()
	resp, err = client.Get(srv.URL)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("next poll: %v %v", resp, err)
	}
	resp.Body.Close()
}

func TestAPIStatsRateLimitHeaders(t *testing.T) {
	remaining := "100"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("RateLimit-Limit", "3600")
		w.Header().Set("RateLimit-Remaining", remaining)
	}))
	defer srv.Close()
	host, _ := url.Parse(srv.URL)

	get := func(stats *APIStats) {
		t.Helper()
		resp, err := stats.HTTPClient().Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	stats := NewAPIStats(5)
	get(stats)
	if v, ok := stats.RateLimitRemaining(); !ok || v != 100 {
		t.Errorf("remaining = %v %v, want 100", v, ok)
	}

	// Invalid values (negative, Inf, NaN) are ignored.
	for _, bad := range []string{"-1", "Inf", "NaN", "1e400"} {
		remaining = bad
		get(stats)
		if v, _ := stats.RateLimitRemaining(); v != 100 {
			t.Errorf("after %q: remaining = %v, want unchanged 100", bad, v)
		}
	}

	// Headers of other hosts (the Storage Box API) are not recorded.
	other := NewAPIStats(5)
	other.SetRateLimitHost("api.hetzner.cloud")
	remaining = "5"
	get(other)
	if _, ok := other.RateLimitRemaining(); ok {
		t.Errorf("headers from %s recorded although only api.hetzner.cloud counts", host.Host)
	}
}

func TestAPIStatsCollect(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(NewAPIStats(5))
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range families {
		names = append(names, f.GetName())
	}
	if !strings.Contains(strings.Join(names, " "), "hetzner_cloud_exporter_api_responses_total") {
		t.Errorf("api_responses_total missing before any request: %v", names)
	}
}

// A 429 from the Storage Box API (own budget) must not block Cloud requests
// or drive the backoff, and Storage Box requests don't use the Cloud budget.
func TestAPIStatsStorageBoxRateLimitSeparate(t *testing.T) {
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer cloud.Close()
	boxes := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer boxes.Close()
	cloudURL, _ := url.Parse(cloud.URL)

	stats := NewAPIStats(5)
	stats.SetRateLimitHost(cloudURL.Host)
	client := stats.HTTPClient()
	get := func(u string) error {
		resp, err := client.Get(u)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}

	stats.StartPoll()
	if err := get(boxes.URL); err != nil {
		t.Fatalf("storage box request: %v", err)
	}
	if err := get(boxes.URL); !errors.Is(err, hcmetrics.ErrRateLimited) {
		t.Errorf("second storage box request: err = %v, want ErrRateLimited", err)
	}
	if err := get(cloud.URL); err != nil {
		t.Errorf("cloud request after a storage box 429: %v, want sent", err)
	}
	if got := stats.RateLimited(); got != 0 {
		t.Errorf("RateLimited = %d, want 0 (storage box 429 must not back off the poller)", got)
	}
	if got, all := stats.BudgetRequests(), stats.Requests(); got != 1 || all != 2 {
		t.Errorf("BudgetRequests = %d, Requests = %d, want 1 and 2", got, all)
	}
}
