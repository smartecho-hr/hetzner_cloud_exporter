package poller

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcmetrics"
)

// APIStats is an http.RoundTripper that counts Hetzner API requests and
// records the rate limit headers returned by the API.
// Pass APIStats.HTTPClient() to hcloud.WithHTTPClient.
//
// Only requests to the rate limit host (the Cloud API) count toward the
// budget, the backoff and the per-hour estimate. The Storage Box API
// (api.hetzner.com) has its own budget: a 429 there only stops further
// requests to that host in the same poll.
type APIStats struct {
	next           http.RoundTripper
	requests       atomic.Uint64 // all requests
	budgetRequests atomic.Uint64 // requests to the rate limit host
	rateLimited    atomic.Uint64 // responses with HTTP 429 from the rate limit host
	rateLimitHost  string        // host whose budget is tracked; "" = all

	mu             sync.Mutex
	blocked        map[string]bool // hosts that answered 429 in the current poll
	rateLimit      float64
	rateRemaining  float64
	rateLimitKnown bool

	requestsDesc      *prometheus.Desc
	rateLimitDesc     *prometheus.Desc
	rateRemainingDesc *prometheus.Desc
	responses         *prometheus.CounterVec
}

// NewAPIStats returns an APIStats. maxConns sets how many idle connections
// per host are kept, at least the request concurrency, so connections (and
// TLS handshakes) are reused.
func NewAPIStats(maxConns int) *APIStats {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = max(maxConns, 2)

	namespace := hcmetrics.Namespace
	a := &APIStats{
		next:    transport,
		blocked: make(map[string]bool),
		requestsDesc: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "exporter", "api_requests_total"),
			"Total number of requests sent to the Hetzner API",
			nil, nil),
		rateLimitDesc: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "exporter", "api_rate_limit"),
			"Hetzner API rate limit (requests per hour), from the last response",
			nil, nil),
		rateRemainingDesc: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "exporter", "api_rate_limit_remaining"),
			"Remaining Hetzner API requests in the current rate limit window, from the last response",
			nil, nil),
		responses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: prometheus.BuildFQName(namespace, "exporter", "api_responses_total"),
			Help: "Total number of Hetzner API responses by HTTP status code",
		}, []string{"code"}),
	}
	// Exists from the start, so increase() in alerts sees the first 429.
	a.responses.WithLabelValues(strconv.Itoa(http.StatusTooManyRequests))
	return a
}

// SetRateLimitHost limits the tracked budget (rate limit headers, requests
// and 429 responses that drive the backoff) to one host, the Cloud API;
// other Hetzner APIs (Storage Boxes) have their own budget.
func (a *APIStats) SetRateLimitHost(host string) {
	a.rateLimitHost = host
}

// StartPoll is called by the poller before every poll. After an HTTP 429,
// further requests of the same poll to that host are not sent (they would
// fail too and use more of the budget); the next poll tries again.
func (a *APIStats) StartPoll() {
	a.mu.Lock()
	clear(a.blocked)
	a.mu.Unlock()
}

// HTTPClient returns an http.Client that records stats for every request.
func (a *APIStats) HTTPClient() *http.Client {
	return &http.Client{Transport: a}
}

// Requests returns the total number of requests sent so far, to all hosts.
func (a *APIStats) Requests() uint64 {
	return a.requests.Load()
}

// BudgetRequests returns the number of requests sent so far to the rate
// limit host, i.e. the requests that use the Cloud API budget.
func (a *APIStats) BudgetRequests() uint64 {
	return a.budgetRequests.Load()
}

// RateLimited returns the number of responses with HTTP 429 from the rate
// limit host so far.
func (a *APIStats) RateLimited() uint64 {
	return a.rateLimited.Load()
}

// RateLimitRemaining returns the remaining requests reported by the last
// response, and false if no response had rate limit headers yet.
func (a *APIStats) RateLimitRemaining() (float64, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rateRemaining, a.rateLimitKnown
}

// RoundTrip implements http.RoundTripper: it sends the request unless the
// host answered 429 earlier in this poll, and records the response.
func (a *APIStats) RoundTrip(req *http.Request) (*http.Response, error) {
	// A request whose context is already cancelled (shutdown) is never sent.
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	host := req.URL.Host
	a.mu.Lock()
	blocked := a.blocked[host]
	a.mu.Unlock()
	if blocked {
		return nil, fmt.Errorf("%s: %w", req.URL.Path, hcmetrics.ErrRateLimited)
	}
	budget := a.rateLimitHost == "" || host == a.rateLimitHost
	a.requests.Add(1)
	if budget {
		a.budgetRequests.Add(1)
	}

	resp, err := a.next.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	a.responses.WithLabelValues(strconv.Itoa(resp.StatusCode)).Inc()
	if resp.StatusCode == http.StatusTooManyRequests {
		a.mu.Lock()
		a.blocked[host] = true
		a.mu.Unlock()
		if budget {
			a.rateLimited.Add(1)
		}
	}

	if !budget {
		return resp, nil
	}
	limit, limitOK := parseCount(resp.Header.Get("RateLimit-Limit"))
	remaining, remainingOK := parseCount(resp.Header.Get("RateLimit-Remaining"))
	if limitOK && remainingOK {
		a.mu.Lock()
		a.rateLimit, a.rateRemaining, a.rateLimitKnown = limit, remaining, true
		a.mu.Unlock()
	}
	return resp, nil
}

// Describe implements prometheus.Collector.
func (a *APIStats) Describe(ch chan<- *prometheus.Desc) {
	ch <- a.requestsDesc
	ch <- a.rateLimitDesc
	ch <- a.rateRemainingDesc
	a.responses.Describe(ch)
}

// Collect implements prometheus.Collector: request counts, the last rate
// limit headers and the responses by status code.
func (a *APIStats) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(a.requestsDesc, prometheus.CounterValue, float64(a.Requests()))

	a.mu.Lock()
	limit, remaining, known := a.rateLimit, a.rateRemaining, a.rateLimitKnown
	a.mu.Unlock()
	if known {
		ch <- prometheus.MustNewConstMetric(a.rateLimitDesc, prometheus.GaugeValue, limit)
		ch <- prometheus.MustNewConstMetric(a.rateRemainingDesc, prometheus.GaugeValue, remaining)
	}

	a.responses.Collect(ch)
}

// parseCount parses a rate limit header; only finite values >= 0 are valid.
func parseCount(header string) (float64, bool) {
	v, err := strconv.ParseFloat(header, 64)
	if err != nil || v < 0 || math.IsInf(v, 0) || math.IsNaN(v) {
		return 0, false
	}
	return v, true
}
