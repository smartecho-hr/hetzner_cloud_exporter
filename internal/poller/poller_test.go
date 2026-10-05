package poller

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcloudtest"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcmetrics"
)

// fakeSource makes `requests` API calls through client on every Fetch.
type fakeSource struct {
	name     string
	err      error
	client   *http.Client
	url      string
	requests int
	fetches  int
}

func (f *fakeSource) Name() string                        { return f.name }
func (f *fakeSource) Describe(ch chan<- *prometheus.Desc) {}
func (f *fakeSource) Collect(ch chan<- prometheus.Metric) {}

func (f *fakeSource) Fetch(ctx context.Context) error {
	f.fetches++
	for range f.requests {
		resp, err := f.client.Get(f.url)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
	}
	return f.err
}

func newAPI(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("RateLimit-Limit", "3600")
		w.Header().Set("RateLimit-Remaining", "3500")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPollRecordsSourceHealth(t *testing.T) {
	api := newAPI(t)
	stats := NewAPIStats(5)
	good := &fakeSource{name: "good", client: stats.HTTPClient(), url: api.URL, requests: 2}
	bad := &fakeSource{name: "bad", client: stats.HTTPClient(), url: api.URL, requests: 1, err: errors.New("boom")}

	p := New([]Source{good, bad}, stats, time.Minute)
	reg := prometheus.NewRegistry()
	p.Register(reg)

	// Two polls at the same (fixed) time with 3 requests each: 6 requests
	// within one poll interval -> 360/h (see TestAPIUsageMeasuredOverAnHour).
	fixed := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return fixed }
	p.poll(context.Background())
	p.poll(context.Background())

	if good.fetches != 2 || bad.fetches != 2 {
		t.Fatalf("fetches = %d/%d, want 2/2", good.fetches, bad.fetches)
	}

	expected := `
# HELP hetzner_cloud_exporter_last_poll_success Whether the last poll of a source succeeded without errors (1=success, 0=failure)
# TYPE hetzner_cloud_exporter_last_poll_success gauge
hetzner_cloud_exporter_last_poll_success{source="bad"} 0
hetzner_cloud_exporter_last_poll_success{source="good"} 1
# HELP hetzner_cloud_exporter_poll_errors_total Total number of failed polls per source
# TYPE hetzner_cloud_exporter_poll_errors_total counter
hetzner_cloud_exporter_poll_errors_total{source="bad"} 2
hetzner_cloud_exporter_poll_errors_total{source="good"} 0
# HELP hetzner_cloud_exporter_api_requests_per_hour_estimate Hetzner Cloud API requests per hour (the rate-limited budget, without Storage Box requests), measured over the last hour (extrapolated during the first hour)
# TYPE hetzner_cloud_exporter_api_requests_per_hour_estimate gauge
hetzner_cloud_exporter_api_requests_per_hour_estimate 360
# HELP hetzner_cloud_exporter_api_requests_total Total number of requests sent to the Hetzner API
# TYPE hetzner_cloud_exporter_api_requests_total counter
hetzner_cloud_exporter_api_requests_total 6
# HELP hetzner_cloud_exporter_api_rate_limit_remaining Remaining Hetzner API requests in the current rate limit window, from the last response
# TYPE hetzner_cloud_exporter_api_rate_limit_remaining gauge
hetzner_cloud_exporter_api_rate_limit_remaining 3500
`
	err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"hetzner_cloud_exporter_last_poll_success",
		"hetzner_cloud_exporter_poll_errors_total",
		"hetzner_cloud_exporter_api_requests_per_hour_estimate",
		"hetzner_cloud_exporter_api_requests_total",
		"hetzner_cloud_exporter_api_rate_limit_remaining",
	)
	if err != nil {
		t.Error(err)
	}

	if got := testutil.ToFloat64(p.lastSuccessTimestamp.WithLabelValues("good")); got == 0 {
		t.Error("last_success_timestamp_seconds for good source not set")
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	src := &fakeSource{name: "src"}
	p := New([]Source{src}, NewAPIStats(5), time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if src.fetches != 1 {
		t.Errorf("fetches = %d, want 1 (immediate first poll)", src.fetches)
	}
}

func TestPartialErrorKeepsSourceFresh(t *testing.T) {
	partial := &fakeSource{name: "partial", err: &hcmetrics.PartialError{Err: errors.New("1 of 3 requests failed")}}
	failed := &fakeSource{name: "failed", err: errors.New("listing failed")}
	p := New([]Source{partial, failed}, NewAPIStats(5), time.Minute)

	p.poll(context.Background())

	if got := testutil.ToFloat64(p.lastSuccess.WithLabelValues("partial")); got != 0 {
		t.Errorf("last_poll_success{partial} = %v, want 0", got)
	}
	if got := testutil.ToFloat64(p.lastSuccessTimestamp.WithLabelValues("partial")); got == 0 {
		t.Error("last_success_timestamp_seconds{partial} not set, but the data is fresh")
	}
	if got := testutil.ToFloat64(p.lastSuccessTimestamp.WithLabelValues("failed")); got != 0 {
		t.Errorf("last_success_timestamp_seconds{failed} = %v, want unset", got)
	}
}

// cancellingSource cancels the poll context while fetching, like SIGTERM
// arriving during a poll.
type cancellingSource struct {
	fakeSource
	cancel context.CancelFunc
}

func (c *cancellingSource) Fetch(ctx context.Context) error {
	c.fetches++
	c.cancel()
	return ctx.Err()
}

func TestPollStopsOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	first := &cancellingSource{fakeSource: fakeSource{name: "first"}, cancel: cancel}
	second := &fakeSource{name: "second"}
	p := New([]Source{first, second}, NewAPIStats(5), time.Minute)

	p.poll(ctx)

	if second.fetches != 0 {
		t.Errorf("second source fetched %d times after shutdown, want 0", second.fetches)
	}
	if got := testutil.ToFloat64(p.pollErrors.WithLabelValues("first")); got != 0 {
		t.Errorf("poll_errors_total{first} = %v, want 0 (shutdown is not an error)", got)
	}
}

func TestReady(t *testing.T) {
	ok := &fakeSource{name: "ok"}
	partial := &fakeSource{name: "partial", err: &hcmetrics.PartialError{Err: errors.New("1 failed")}}
	failing := &fakeSource{name: "failing", err: errors.New("list failed")}
	p := New([]Source{ok, partial, failing}, NewAPIStats(5), time.Minute)

	if ready, missing := p.Ready(); ready || len(missing) != 3 {
		t.Errorf("before first poll: ready=%v missing=%v, want not ready, all missing", ready, missing)
	}

	p.poll(context.Background())
	if ready, missing := p.Ready(); ready || len(missing) != 1 || missing[0] != "failing (error, see log)" {
		t.Errorf("after poll: ready=%v missing=%v, want only failing missing", ready, missing)
	}

	failing.err = nil
	p.poll(context.Background())
	if ready, _ := p.Ready(); !ready {
		t.Error("all sources fetched once: want ready")
	}

	// Once ready, a later failure doesn't make it unready (data is still there).
	failing.err = errors.New("list failed again")
	p.poll(context.Background())
	if ready, _ := p.Ready(); !ready {
		t.Error("after a later failure: want still ready")
	}
}

// statusAPI answers every request with the given status code and remaining
// rate limit.
func statusAPI(t *testing.T, status *int, remaining *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("RateLimit-Limit", "3600")
		w.Header().Set("RateLimit-Remaining", *remaining)
		w.WriteHeader(*status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBackoffOnRateLimit(t *testing.T) {
	status, remaining := http.StatusTooManyRequests, "0"
	api := statusAPI(t, &status, &remaining)
	stats := NewAPIStats(5)
	src := &fakeSource{name: "src", client: stats.HTTPClient(), url: api.URL, requests: 1}
	p := New([]Source{src}, stats, time.Minute)

	// 429: the interval doubles per poll, up to 16x the configured interval.
	for _, want := range []time.Duration{2, 4, 8, 16, 16} {
		p.poll(context.Background())
		if got := p.nextWait(); got != want*time.Minute {
			t.Fatalf("after 429: wait = %v, want %v", got, want*time.Minute)
		}
	}
	if got := testutil.ToFloat64(p.pollInterval); got != (16 * time.Minute).Seconds() {
		t.Errorf("poll_interval_seconds = %v, want %v", got, (16 * time.Minute).Seconds())
	}

	// Recovery: halves per poll without 429, back to the configured interval.
	status, remaining = http.StatusOK, "3000"
	for _, want := range []time.Duration{8, 4, 2, 1, 1} {
		p.poll(context.Background())
		if got := p.nextWait(); got != want*time.Minute {
			t.Fatalf("recovering: wait = %v, want %v", got, want*time.Minute)
		}
	}
}

func TestWaitsForRateLimitBudget(t *testing.T) {
	status, remaining := http.StatusOK, "10"
	api := statusAPI(t, &status, &remaining)
	stats := NewAPIStats(5)
	// A poll needs 100 requests, but only 10 are left: Hetzner refills one
	// per second, so the next poll must wait at least 90 seconds.
	src := &fakeSource{name: "src", client: stats.HTTPClient(), url: api.URL, requests: 100}
	p := New([]Source{src}, stats, 30*time.Second)

	p.poll(context.Background())
	if got := p.nextWait(); got != 90*time.Second {
		t.Errorf("wait = %v, want 90s", got)
	}
}

type throttledSource struct {
	fakeSource
	every time.Duration
}

func (s *throttledSource) MinInterval() time.Duration { return s.every }

func TestThrottledSource(t *testing.T) {
	slow := &throttledSource{fakeSource: fakeSource{name: "slow", err: errors.New("down")}, every: time.Hour}
	fast := &fakeSource{name: "fast"}
	p := New([]Source{slow, fast}, NewAPIStats(5), time.Minute)

	// Retried on every poll until it succeeds once.
	p.poll(context.Background())
	p.poll(context.Background())
	if slow.fetches != 2 {
		t.Fatalf("failing throttled source fetched %d times, want 2", slow.fetches)
	}

	// After a success it waits for its interval; other sources keep polling.
	slow.err = nil
	p.poll(context.Background())
	p.poll(context.Background())
	p.poll(context.Background())
	if slow.fetches != 3 || fast.fetches != 5 {
		t.Errorf("fetches slow=%d fast=%d, want 3 and 5", slow.fetches, fast.fetches)
	}

	// Due again once the interval has passed.
	p.mu.Lock()
	p.lastFetch["slow"] = time.Now().Add(-2 * time.Hour)
	p.mu.Unlock()
	p.poll(context.Background())
	if slow.fetches != 4 {
		t.Errorf("after interval: slow fetched %d times, want 4", slow.fetches)
	}
}

func TestAPIUsageMeasuredOverAnHour(t *testing.T) {
	p := New(nil, NewAPIStats(5), time.Minute)
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	// First poll: 10 requests, extrapolated over one interval -> 600/h.
	p.recordAPIUsage(start, 10)
	if got := testutil.ToFloat64(p.apiRequestsHr); got != 600 {
		t.Errorf("after first poll: %v/h, want 600", got)
	}

	// Polls alternate between 10 (with metrics) and 2 (lists only) requests
	// every minute: 6 per poll on average -> 360/h, not 600 or 120.
	for i := 1; i < 120; i++ {
		requests := uint64(2)
		if i%2 == 0 {
			requests = 10
		}
		p.recordAPIUsage(start.Add(time.Duration(i)*time.Minute), requests)
	}
	if got := testutil.ToFloat64(p.apiRequestsHr); got != 360 {
		t.Errorf("after two hours: %v/h, want 360", got)
	}
	if len(p.history) != 61 {
		t.Errorf("history keeps %d polls, want the last hour plus the one before (61)", len(p.history))
	}

	// Polls further apart than the configured interval (long intervals,
	// backoff after 429): the rate uses the real spacing.
	for _, tt := range []struct {
		interval, spacing time.Duration
		want              float64
	}{
		{2 * time.Hour, 2 * time.Hour, 50},
		{45 * time.Minute, 45 * time.Minute, 100 * 60 / 45.0},
		{time.Minute, 16 * time.Minute, 100 * 60 / 16.0},
	} {
		p := New(nil, NewAPIStats(5), tt.interval)
		for i := range 20 {
			p.recordAPIUsage(start.Add(time.Duration(i)*tt.spacing), 100)
		}
		if got := testutil.ToFloat64(p.apiRequestsHr); math.Abs(got-tt.want) > 0.01 {
			t.Errorf("interval %v, polls every %v: %.2f/h, want %.2f", tt.interval, tt.spacing, got, tt.want)
		}
	}
}

// The first poll (hourly sources, a full metrics round) extrapolates far
// above the limit; the warning waits for a full hour and is logged once.
func TestAPIUsageWarningAfterAnHour(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	warnings := func() int {
		n := 0
		for _, e := range hook.AllEntries() {
			if e.Level == log.WarnLevel && strings.Contains(e.Message, "exceeds the rate limit") {
				n++
			}
		}
		return n
	}

	p := New(nil, NewAPIStats(5), time.Minute)
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	p.recordAPIUsage(start, 200) // 12000/h extrapolated
	for i := 1; i < 60; i++ {
		p.recordAPIUsage(start.Add(time.Duration(i)*time.Minute), 10)
	}
	if n := warnings(); n != 0 {
		t.Errorf("warnings during the first hour = %d, want 0", n)
	}

	// Really over the limit for more than an hour: one warning, not one per poll.
	for i := 60; i < 130; i++ {
		p.recordAPIUsage(start.Add(time.Duration(i)*time.Minute), 100)
	}
	if n := warnings(); n != 1 {
		t.Errorf("warnings while over the limit = %d, want 1", n)
	}
}

func TestBudgetWaitDoesNotStick(t *testing.T) {
	remaining := "500"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("RateLimit-Limit", "3600")
		w.Header().Set("RateLimit-Remaining", remaining)
	}))
	defer srv.Close()
	stats := NewAPIStats(5)
	src := &fakeSource{name: "src", client: stats.HTTPClient(), url: srv.URL, requests: 600}
	p := New([]Source{src}, stats, time.Minute)

	// 600 requests with 500 left: wait for 100 refilled requests.
	p.poll(context.Background())
	if got := p.nextWait(); got != 100*time.Second {
		t.Fatalf("budget wait = %v, want 100s", got)
	}

	// Budget full again: back to the configured interval at once.
	remaining = "3500"
	src.requests = 10
	p.poll(context.Background())
	if got := p.nextWait(); got != time.Minute {
		t.Errorf("after recovery: wait = %v, want 1m (budget wait must not stick)", got)
	}

	// A huge deficit is capped at one hour (the bucket refills completely).
	remaining = "0"
	src.requests = 4000 // 4000s deficit, more than the 1h cap
	p.poll(context.Background())
	if got := p.nextWait(); got != time.Hour {
		t.Errorf("huge deficit: wait = %v, want 1h", got)
	}
}

func TestSourceIntervalMetric(t *testing.T) {
	fast := &fakeSource{name: "fast"}
	slow := &throttledSource{fakeSource: fakeSource{name: "slow"}, every: time.Hour}
	p := New([]Source{fast, slow}, NewAPIStats(5), time.Minute)
	p.Register(prometheus.NewRegistry())

	if got := testutil.ToFloat64(p.sourceInterval.WithLabelValues("fast")); got != 60 {
		t.Errorf("fast = %v, want 60", got)
	}
	if got := testutil.ToFloat64(p.sourceInterval.WithLabelValues("slow")); got != 3600 {
		t.Errorf("slow = %v, want 3600", got)
	}
}

// funcSource is a Source whose Fetch runs fn.
type funcSource struct {
	name string
	fn   func(ctx context.Context) error
}

func (s funcSource) Name() string                     { return s.name }
func (s funcSource) Fetch(ctx context.Context) error  { return s.fn(ctx) }
func (s funcSource) Describe(chan<- *prometheus.Desc) {}
func (s funcSource) Collect(chan<- prometheus.Metric) {}

// /ready names the reason of each failing source, from real hcloud errors.
func TestReadyReasons(t *testing.T) {
	fake, client := hcloudtest.NewServer(t, map[string]string{
		"/ssh_keys": `{"ssh_keys": [], ` + hcloudtest.Pagination + `}`,
	})
	fake.Status("/ssh_keys", http.StatusForbidden)
	fake.Status("/volumes", http.StatusTooManyRequests)
	fake.Status("/certificates", http.StatusUnauthorized)

	sources := []Source{
		funcSource{"forbidden", func(ctx context.Context) error { _, err := client.SSHKey.All(ctx); return err }},
		funcSource{"limited", func(ctx context.Context) error { _, err := client.Volume.All(ctx); return err }},
		funcSource{"unauthorized", func(ctx context.Context) error { _, err := client.Certificate.All(ctx); return err }},
		funcSource{"timeout", func(context.Context) error { return context.DeadlineExceeded }},
		funcSource{"skipped", func(context.Context) error { return hcmetrics.ErrRateLimited }},
	}
	p := New(sources, NewAPIStats(5), time.Minute)
	p.poll(context.Background())

	_, missing := p.Ready()
	want := []string{
		"forbidden (forbidden: the token can't access this resource)",
		"limited (rate limited)",
		"unauthorized (unauthorized: check the token)",
		"timeout (timeout)",
		"skipped (rate limited)",
	}
	if strings.Join(missing, "|") != strings.Join(want, "|") {
		t.Errorf("missing = %q,\nwant      %q", missing, want)
	}

	fake.Recover("/ssh_keys")
	p.poll(context.Background())
	if _, missing := p.Ready(); len(missing) != 4 {
		t.Errorf("after recovery: missing = %q, want 4", missing)
	}
}
