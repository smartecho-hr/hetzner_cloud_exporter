// Package poller fetches data for all sources from the Hetzner API in the
// background and keeps track of API usage and rate limits.
package poller

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"

	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcmetrics"
)

// Hetzner Cloud API allows 3600 requests per hour per project and refills
// one request per second.
const apiRequestsPerHourLimit = 3600

// maxBackoffFactor limits how far the poll interval grows on HTTP 429.
const maxBackoffFactor = 16

// Source is one kind of Hetzner resource (servers, load balancers, ...).
//
// Fetch is called by the Poller on every interval and stores the data inside
// the source. Collect (from prometheus.Collector) turns the stored data into
// metrics on every scrape and must never call the Hetzner API.
type Source interface {
	prometheus.Collector

	// Name identifies the source in logs and in the exporter's own metrics.
	Name() string

	// Fetch refreshes the source's data. If the resource list was fetched but
	// some per-item requests failed, it stores what it got and returns a
	// *hcmetrics.PartialError. If the list failed, it keeps the previous data.
	Fetch(ctx context.Context) error
}

// Throttled is implemented by sources whose data changes rarely (e.g.
// prices). The poller fetches them at most once per MinInterval; until a
// fetch succeeded, it retries on every poll.
type Throttled interface {
	MinInterval() time.Duration
}

// Poller periodically fetches data for all sources in the background,
// so that Prometheus scrapes never hit the Hetzner API directly.
type Poller struct {
	sources  []Source
	api      *APIStats
	interval time.Duration // configured interval

	mu        sync.Mutex
	now       func() time.Time     // time.Now, replaceable in tests
	history   []pollRecord         // requests per poll during the last hour
	started   time.Time            // first poll, for the per-hour estimate
	overLimit bool                 // last estimate (over a full hour) exceeded the limit
	current   time.Duration        // interval in use, longer while backing off after HTTP 429
	wait      time.Duration        // wait before the next poll: current, or longer if the budget is low
	fresh     map[string]bool      // sources whose list was fetched at least once
	lastError map[string]string    // short reason of the last failed poll per source, for /ready
	lastFetch map[string]time.Time // last successful fetch, for Throttled sources

	lastSuccess          *prometheus.GaugeVec
	lastSuccessTimestamp *prometheus.GaugeVec
	pollErrors           *prometheus.CounterVec
	pollDuration         prometheus.Gauge
	apiRequestsHr        prometheus.Gauge
	pollInterval         prometheus.Gauge
	sourceInterval       *prometheus.GaugeVec
}

// New returns a Poller that fetches sources every interval (longer while
// backing off) and counts their API requests in api. Call Register, then Run.
func New(sources []Source, api *APIStats, interval time.Duration) *Poller {
	namespace := hcmetrics.Namespace
	return &Poller{
		sources:   sources,
		api:       api,
		interval:  interval,
		now:       time.Now,
		current:   interval,
		wait:      interval,
		fresh:     make(map[string]bool),
		lastError: make(map[string]string),
		lastFetch: make(map[string]time.Time),
		lastSuccess: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: prometheus.BuildFQName(namespace, "exporter", "last_poll_success"),
			Help: "Whether the last poll of a source succeeded without errors (1=success, 0=failure)",
		}, []string{"source"}),
		lastSuccessTimestamp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: prometheus.BuildFQName(namespace, "exporter", "last_success_timestamp_seconds"),
			Help: "Unix timestamp of the last poll that refreshed a source's data (also when some per-item requests failed)",
		}, []string{"source"}),
		pollErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: prometheus.BuildFQName(namespace, "exporter", "poll_errors_total"),
			Help: "Total number of failed polls per source",
		}, []string{"source"}),
		pollDuration: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: prometheus.BuildFQName(namespace, "exporter", "poll_duration_seconds"),
			Help: "Duration of the last poll of all sources",
		}),
		apiRequestsHr: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: prometheus.BuildFQName(namespace, "exporter", "api_requests_per_hour_estimate"),
			Help: "Hetzner Cloud API requests per hour (the rate-limited budget, without Storage Box requests), measured over the last hour (extrapolated during the first hour)",
		}),
		sourceInterval: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: prometheus.BuildFQName(namespace, "exporter", "source_interval_seconds"),
			Help: "How often a source is fetched: the poll interval, or longer for sources whose data changes rarely",
		}, []string{"source"}),
		pollInterval: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: prometheus.BuildFQName(namespace, "exporter", "poll_interval_seconds"),
			Help: "Wait until the next poll; longer than configured while backing off after HTTP 429 or when the API budget is low",
		}),
	}
}

// Ready reports whether every source has fetched its data at least once, and
// if not, which sources are still missing (with the reason of their last
// failure, e.g. "servers (unauthorized)").
func (p *Poller) Ready() (bool, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var missing []string
	for _, source := range p.sources {
		name := source.Name()
		if p.fresh[name] {
			continue
		}
		if reason := p.lastError[name]; reason != "" {
			name += " (" + reason + ")"
		}
		missing = append(missing, name)
	}
	return len(missing) == 0, missing
}

// Register registers the poller's own metrics, the API stats and all sources.
func (p *Poller) Register(reg prometheus.Registerer) {
	reg.MustRegister(p.lastSuccess, p.lastSuccessTimestamp, p.pollErrors, p.pollDuration, p.apiRequestsHr, p.pollInterval, p.sourceInterval)
	p.pollInterval.Set(p.interval.Seconds())
	reg.MustRegister(p.api)
	for _, source := range p.sources {
		// Initialise per-source series so they exist before the first poll.
		p.lastSuccess.WithLabelValues(source.Name())
		p.pollErrors.WithLabelValues(source.Name())
		interval := p.interval
		if throttled, ok := source.(Throttled); ok {
			interval = max(interval, throttled.MinInterval())
		}
		p.sourceInterval.WithLabelValues(source.Name()).Set(interval.Seconds())
		reg.MustRegister(source)
	}
}

// Run polls immediately and then after every interval until ctx is cancelled.
// The interval grows after HTTP 429 responses and when the remaining API
// budget can't cover the next poll (see nextWait).
func (p *Poller) Run(ctx context.Context) {
	for {
		p.poll(ctx)

		timer := time.NewTimer(p.nextWait())
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// nextWait returns how long to wait before the next poll (set by adjustInterval).
func (p *Poller) nextWait() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.wait
}

func (p *Poller) poll(ctx context.Context) {
	started := p.now()
	p.api.StartPoll()
	requestsBefore := p.api.BudgetRequests()
	rateLimitedBefore := p.api.RateLimited()
	ok := true
	var unauthorized []string

	for _, source := range p.sources {
		if p.skip(source, started) {
			continue
		}
		err := source.Fetch(ctx)
		if ctx.Err() != nil {
			return // shutting down: not a source failure, don't record anything
		}

		name := source.Name()
		if err != nil {
			ok = false
			p.lastSuccess.WithLabelValues(name).Set(0)
			p.pollErrors.WithLabelValues(name).Inc()
			p.setLastError(name, err)
			if hcloud.IsError(err, hcloud.ErrorCodeUnauthorized) {
				unauthorized = append(unauthorized, name) // one summary line below
			} else {
				log.WithError(err).WithField("source", name).Error("Failed to poll source")
			}
		} else {
			p.lastSuccess.WithLabelValues(name).Set(1)
			p.setLastError(name, nil)
		}

		// The data is fresh as long as the resource list was fetched.
		if err == nil || hcmetrics.IsPartial(err) {
			p.lastSuccessTimestamp.WithLabelValues(name).SetToCurrentTime()
			p.mu.Lock()
			p.fresh[name] = true
			p.lastFetch[name] = started
			p.mu.Unlock()
		}
	}

	if len(unauthorized) > 0 {
		log.WithField("sources", strings.Join(unauthorized, ",")).Error(
			"Hetzner API rejected the token (401 unauthorized): check HCLOUD_API_TOKEN / api.token and that the token belongs to the project")
	}

	duration := p.now().Sub(started)
	p.pollDuration.Set(duration.Seconds())
	requests := p.api.BudgetRequests() - requestsBefore
	p.recordAPIUsage(started, requests)
	p.adjustInterval(requests, p.api.RateLimited()-rateLimitedBefore)

	log.WithFields(log.Fields{
		"duration": duration.Round(time.Millisecond),
		"success":  ok,
	}).Info("Polled Hetzner API")
}

type pollRecord struct {
	at       time.Time
	requests uint64
}

// recordAPIUsage adds the requests of a poll started at `at` and updates the
// requests-per-hour gauge. Polls differ in size (metrics intervals, throttled
// sources, backoff), so the rate is measured: the requests of the recorded
// polls divided by the time they cover (from the oldest to this poll), over
// at least the last hour. With a single poll, it covers the current poll
// interval. The first polls include hourly sources and a full metrics round,
// so early estimates are high; the warning waits until a full hour was measured.
func (p *Poller) recordAPIUsage(at time.Time, requests uint64) {
	if p.started.IsZero() {
		p.started = at
	}
	p.history = append(p.history, pollRecord{at: at, requests: requests})
	// Keep the polls of the last hour, plus the one before them, so the
	// window always covers an hour (or the real spacing if polls are rarer).
	for len(p.history) > 2 && at.Sub(p.history[1].at) >= time.Hour {
		p.history = p.history[1:]
	}

	var requestsPerHour float64
	if len(p.history) == 1 || !at.After(p.history[0].at) {
		// One poll, or polls without time between them: extrapolate over
		// the poll interval.
		var sum uint64
		for _, r := range p.history {
			sum += r.requests
		}
		requestsPerHour = float64(sum) * time.Hour.Seconds() / p.current.Seconds()
	} else {
		// Each poll's requests cover the time until the next poll.
		var sum uint64
		for _, r := range p.history[:len(p.history)-1] {
			sum += r.requests
		}
		span := at.Sub(p.history[0].at)
		requestsPerHour = float64(sum) * time.Hour.Seconds() / span.Seconds()
	}

	p.apiRequestsHr.Set(requestsPerHour)
	if at.Sub(p.started) < time.Hour {
		return
	}
	over := requestsPerHour > apiRequestsPerHourLimit
	if over && !p.overLimit {
		log.WithFields(log.Fields{
			"estimate": int(requestsPerHour),
			"limit":    apiRequestsPerHourLimit,
			"interval": p.interval,
		}).Warn("Hetzner API usage exceeds the rate limit; increase --hcloud.poll-interval or --hcloud.metrics-interval, or turn off collectors")
	}
	if !over && p.overLimit {
		log.WithField("estimate", int(requestsPerHour)).Info("Hetzner API usage is within the rate limit again")
	}
	p.overLimit = over
}

// setLastError remembers a short reason for /ready; nil clears it.
func (p *Poller) setLastError(name string, err error) {
	reason := ""
	switch {
	case err == nil:
	case hcloud.IsError(err, hcloud.ErrorCodeUnauthorized):
		reason = "unauthorized: check the token"
	case hcloud.IsError(err, hcloud.ErrorCodeForbidden):
		reason = "forbidden: the token can't access this resource"
	case errors.Is(err, hcmetrics.ErrRateLimited), hcloud.IsError(err, hcloud.ErrorCodeRateLimitExceeded):
		reason = "rate limited"
	case errors.Is(err, context.DeadlineExceeded):
		reason = "timeout"
	default:
		reason = "error, see log"
	}
	p.mu.Lock()
	p.lastError[name] = reason
	p.mu.Unlock()
}

// skip reports whether a Throttled source was fetched recently enough.
func (p *Poller) skip(source Source, now time.Time) bool {
	throttled, ok := source.(Throttled)
	if !ok {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	last, fetched := p.lastFetch[source.Name()]
	return fetched && now.Sub(last) < throttled.MinInterval()
}

// adjustInterval doubles the poll interval (up to maxBackoffFactor times the
// configured one) when the last poll got HTTP 429 responses, and halves it
// back towards the configured interval after polls without.
// It also makes sure the remaining API budget covers the next poll: Hetzner
// refills one request per second, so a deficit of n requests means waiting
// n seconds longer.
func (p *Poller) adjustInterval(requests, rateLimited uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	previous := p.wait
	switch {
	case rateLimited > 0:
		p.current = min(p.current*2, p.interval*maxBackoffFactor)
	case p.current > p.interval:
		p.current = max(p.current/2, p.interval)
	}

	// A low budget only delays the next poll; it doesn't change the backoff.
	// Hetzner refills one request per second, so the bucket is full after an
	// hour at most.
	wait := p.current
	if remaining, ok := p.api.RateLimitRemaining(); ok && float64(requests) > remaining {
		deficit := min(time.Duration(float64(requests)-remaining)*time.Second, time.Hour)
		wait = max(wait, deficit)
	}
	p.wait = wait
	p.pollInterval.Set(wait.Seconds())

	if wait != previous {
		entry := log.WithFields(log.Fields{
			"interval":     wait,
			"configured":   p.interval,
			"rate_limited": rateLimited,
		})
		if wait == p.interval {
			entry.Info("Hetzner API rate limit: poll interval back to normal")
		} else {
			entry.Warn("Hetzner API rate limit: adjusting poll interval")
		}
	}
}
