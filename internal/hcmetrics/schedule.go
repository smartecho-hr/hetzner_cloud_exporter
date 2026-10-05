package hcmetrics

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// MetricsPhaseTimeout bounds fetching the per-resource metrics of one source
// in one poll, so a hanging metrics endpoint can't stall the whole poll.
// Items not fetched in time are retried on the next poll.
const MetricsPhaseTimeout = 2 * time.Minute

// ErrRateLimited is returned for requests that were not sent because the
// API answered HTTP 429 earlier in the same poll.
var ErrRateLimited = errors.New("rate limit of the Hetzner API reached, request skipped until the next poll")

// MetricsSchedule decides which per-resource metrics (one API request per
// server or Load Balancer) are fetched in a poll, and keeps the last fetched
// metrics per resource ID. The list data of a source is fetched on every
// poll regardless. Not safe for concurrent use; sources call it from Fetch.
type MetricsSchedule[M any] struct {
	interval time.Duration    // 0 = metrics are off
	last     time.Time        // last full fetch
	previous map[int64]M      // last good metrics per resource
	state    map[int64]string // resource state at its last good fetch, e.g. server status
	retries  map[int64]int    // failed fetches since the last good one
}

// NewMetricsSchedule returns a schedule that fetches all metrics at most
// once per interval; an interval of 0 turns metrics off.
func NewMetricsSchedule[M any](interval time.Duration) *MetricsSchedule[M] {
	return &MetricsSchedule[M]{
		interval: interval,
		previous: make(map[int64]M),
		state:    make(map[int64]string),
		retries:  make(map[int64]int),
	}
}

// Due reports whether all metrics should be fetched in a poll starting at now.
func (s *MetricsSchedule[M]) Due(now time.Time) bool {
	return s.interval > 0 && (s.last.IsZero() || now.Sub(s.last) >= s.interval)
}

// needs reports whether a resource has to be fetched in a poll that is not
// a full fetch: it is new since the last fetch, its state changed (e.g. a
// server was started), or its last fetch failed. A failed resource is
// retried once, on the next poll; if that fails too, it waits for the next
// full fetch, so an outage doesn't cost requests on every poll.
func (s *MetricsSchedule[M]) needs(id int64, state string) bool {
	if s.interval <= 0 || s.last.IsZero() {
		return false
	}
	if n := s.retries[id]; n > 0 {
		return n == 1
	}
	_, known := s.previous[id]
	return !known || s.state[id] != state
}

// Previous returns the last fetched metrics of a resource, or the zero value.
func (s *MetricsSchedule[M]) Previous(id int64) M {
	return s.previous[id]
}

// FetchMetrics fetches the metrics of the items that need them in a poll
// starting at now (all items when due, otherwise new, changed and failed
// ones, see needs) and returns the metrics to expose per item, in item
// order. state returns an item's state (e.g. the server status) and may be
// nil. A failed item keeps its previous metrics. Failures are summarised in
// a *PartialError; shutdown returns ctx.Err().
func FetchMetrics[T, M any](
	ctx context.Context,
	now time.Time,
	s *MetricsSchedule[M],
	items []T,
	id func(T) int64,
	state func(T) string,
	limit int,
	fetch func(context.Context, T) (M, error),
) ([]M, error) {
	stateOf := func(item T) string {
		if state == nil {
			return ""
		}
		return state(item)
	}

	full := s.Due(now)
	var selected []T
	for _, item := range items {
		if full || s.needs(id(item), stateOf(item)) {
			selected = append(selected, item)
		}
	}

	var results []M
	var errs []error
	if len(selected) > 0 {
		phaseCtx, cancel := context.WithTimeout(ctx, MetricsPhaseTimeout)
		results, errs = fetchEach(phaseCtx, selected, limit, fetch)
		cancel()
		if ctx.Err() != nil {
			return nil, ctx.Err() // shutting down
		}
	}

	if full {
		// Forget resources that no longer exist (also when none are left).
		s.last = now
		current := make(map[int64]bool, len(items))
		for _, item := range items {
			current[id(item)] = true
		}
		for _, known := range []map[int64]bool{keys(s.previous), keys(s.retries)} {
			for k := range known {
				if !current[k] {
					delete(s.previous, k)
					delete(s.state, k)
					delete(s.retries, k)
				}
			}
		}
	}

	var failed []error
	for i, item := range selected {
		key := id(item)
		if errs[i] != nil {
			if full {
				s.retries[key] = 1 // one retry on the next poll
			} else {
				s.retries[key]++
			}
			failed = append(failed, errs[i])
			continue
		}
		s.previous[key] = results[i]
		s.state[key] = stateOf(item)
		delete(s.retries, key)
	}

	out := make([]M, len(items))
	for i, item := range items {
		out[i] = s.previous[id(item)]
	}
	if len(failed) > 0 {
		return out, &PartialError{Err: summarize(failed, len(selected))}
	}
	return out, nil
}

// keys returns the keys of m as a set, so m can be changed while iterating.
func keys[V any](m map[int64]V) map[int64]bool {
	set := make(map[int64]bool, len(m))
	for k := range m {
		set[k] = true
	}
	return set
}

// summarize describes failed requests in one error, e.g. "3 of 50 metrics
// requests failed (2 timed out), first error: ...".
func summarize(errs []error, total int) error {
	var timeouts, rateLimited int
	for _, err := range errs {
		switch {
		case errors.Is(err, ErrRateLimited):
			rateLimited++
		case errors.Is(err, context.DeadlineExceeded):
			timeouts++
		}
	}
	detail := ""
	if timeouts > 0 {
		detail += fmt.Sprintf(", %d timed out", timeouts)
	}
	if rateLimited > 0 {
		detail += fmt.Sprintf(", %d skipped because of the rate limit", rateLimited)
	}
	return fmt.Errorf("%d of %d metrics requests failed%s, first error: %w", len(errs), total, detail, errs[0])
}
