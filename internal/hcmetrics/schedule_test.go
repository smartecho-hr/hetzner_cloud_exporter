package hcmetrics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type item struct{ id int64 }

func itemID(i item) int64 { return i.id }

// fakeFetch returns "m<id>/<n>" where n counts the fetches of that item;
// items in fail return an error.
type fakeFetch struct {
	mu    sync.Mutex
	calls map[int64]int
	fail  map[int64]error
}

func newFakeFetch() *fakeFetch {
	return &fakeFetch{calls: map[int64]int{}, fail: map[int64]error{}}
}

func (f *fakeFetch) fetch(_ context.Context, i item) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[i.id]++
	if err := f.fail[i.id]; err != nil {
		return "", err
	}
	return fmt.Sprintf("m%d/%d", i.id, f.calls[i.id]), nil
}

func TestFetchMetricsInterval(t *testing.T) {
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s := NewMetricsSchedule[string](2 * time.Minute)
	f := newFakeFetch()
	items := []item{{1}, {2}}

	got, err := FetchMetrics(context.Background(), start, s, items, itemID, nil, 2, f.fetch)
	if err != nil || got[0] != "m1/1" || got[1] != "m2/1" {
		t.Fatalf("first poll: %v %v", got, err)
	}

	// Within the interval: no requests, previous values kept.
	got, _ = FetchMetrics(context.Background(), start.Add(time.Minute), s, items, itemID, nil, 2, f.fetch)
	if got[0] != "m1/1" || f.calls[1] != 1 {
		t.Errorf("within interval: %v, calls %v", got, f.calls)
	}

	// A new resource is fetched right away, without waiting for the interval.
	items = append(items, item{3})
	got, _ = FetchMetrics(context.Background(), start.Add(time.Minute), s, items, itemID, nil, 2, f.fetch)
	if got[2] != "m3/1" || f.calls[1] != 1 {
		t.Errorf("new resource: %v, calls %v", got, f.calls)
	}

	// Due again: everything is fetched; removed resources are forgotten.
	items = []item{{1}, {3}}
	got, _ = FetchMetrics(context.Background(), start.Add(2*time.Minute), s, items, itemID, nil, 2, f.fetch)
	if got[0] != "m1/2" || got[1] != "m3/2" || s.Previous(2) != "" {
		t.Errorf("due again: %v, previous(2)=%q", got, s.Previous(2))
	}
}

func TestFetchMetricsRetriesFailedItems(t *testing.T) {
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s := NewMetricsSchedule[string](10 * time.Minute)
	f := newFakeFetch()
	items := []item{{1}, {2}}

	if _, err := FetchMetrics(context.Background(), start, s, items, itemID, nil, 2, f.fetch); err != nil {
		t.Fatal(err)
	}

	// Item 2 fails on the next full fetch: it keeps its previous value.
	f.fail[2] = errors.New("boom")
	got, err := FetchMetrics(context.Background(), start.Add(10*time.Minute), s, items, itemID, nil, 2, f.fetch)
	if !IsPartial(err) || !strings.Contains(err.Error(), "1 of 2 metrics requests failed") {
		t.Errorf("err = %v, want partial error with summary", err)
	}
	if got[0] != "m1/2" || got[1] != "m2/1" {
		t.Errorf("after failure: %v, want new value for 1 and previous for 2", got)
	}

	// Next poll, long before the interval: only the failed item is retried.
	delete(f.fail, 2)
	got, err = FetchMetrics(context.Background(), start.Add(11*time.Minute), s, items, itemID, nil, 2, f.fetch)
	if err != nil || got[1] != "m2/3" || f.calls[1] != 2 {
		t.Errorf("retry: %v %v, calls %v (item 1 must not be fetched again)", got, err, f.calls)
	}
}

func TestFetchMetricsOff(t *testing.T) {
	s := NewMetricsSchedule[string](0)
	f := newFakeFetch()
	got, err := FetchMetrics(context.Background(), time.Now(), s, []item{{1}}, itemID, nil, 2, f.fetch)
	if err != nil || got[0] != "" || len(f.calls) != 0 {
		t.Errorf("off: %v %v calls %v", got, err, f.calls)
	}
}

func TestFetchMetricsSummarizesRateLimit(t *testing.T) {
	s := NewMetricsSchedule[string](time.Minute)
	f := newFakeFetch()
	f.fail[1] = fmt.Errorf("x: %w", ErrRateLimited)
	f.fail[2] = fmt.Errorf("y: %w", context.DeadlineExceeded)
	_, err := FetchMetrics(context.Background(), time.Now(), s, []item{{1}, {2}, {3}}, itemID, nil, 3, f.fetch)
	if err == nil || !strings.Contains(err.Error(), "1 timed out") || !strings.Contains(err.Error(), "1 skipped because of the rate limit") {
		t.Errorf("err = %v", err)
	}
}

func TestFetchMetricsShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := NewMetricsSchedule[string](time.Minute)
	f := newFakeFetch()
	if _, err := FetchMetrics(ctx, time.Now(), s, []item{{1}}, itemID, nil, 1, f.fetch); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if !s.Due(time.Now()) {
		t.Error("an aborted fetch must not count as done")
	}
}

// A failed item is retried once on the next poll; if that fails too, it
// waits for the next full fetch instead of costing a request on every poll.
func TestFetchMetricsRetriesOnlyOnce(t *testing.T) {
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s := NewMetricsSchedule[string](5 * time.Minute)
	f := newFakeFetch()
	items := []item{{1}}
	f.fail[1] = errors.New("boom")

	for minute := range 5 {
		_, _ = FetchMetrics(context.Background(), start.Add(time.Duration(minute)*time.Minute), s, items, itemID, nil, 1, f.fetch)
	}
	if f.calls[1] != 2 {
		t.Errorf("calls during the interval = %d, want 2 (full fetch + one retry)", f.calls[1])
	}

	// The next full fetch tries again, and then one retry again.
	for minute := 5; minute < 10; minute++ {
		_, _ = FetchMetrics(context.Background(), start.Add(time.Duration(minute)*time.Minute), s, items, itemID, nil, 1, f.fetch)
	}
	if f.calls[1] != 4 {
		t.Errorf("calls after the second interval = %d, want 4", f.calls[1])
	}

	// A failing item that appears between full fetches is also retried only once.
	items = append(items, item{2})
	f.fail[2] = errors.New("boom")
	for minute := 11; minute < 15; minute++ {
		_, _ = FetchMetrics(context.Background(), start.Add(time.Duration(minute)*time.Minute), s, items, itemID, nil, 1, f.fetch)
	}
	if f.calls[2] != 2 {
		t.Errorf("new failing item: calls = %d, want 2 (first fetch + one retry)", f.calls[2])
	}
}

// An item whose state changed (a server started since the last full fetch)
// is fetched again on the next poll instead of keeping stale metrics.
func TestFetchMetricsRefetchesOnStateChange(t *testing.T) {
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s := NewMetricsSchedule[string](10 * time.Minute)
	f := newFakeFetch()
	status := map[int64]string{1: "off"}
	state := func(i item) string { return status[i.id] }
	items := []item{{1}}

	if _, err := FetchMetrics(context.Background(), start, s, items, itemID, state, 1, f.fetch); err != nil {
		t.Fatal(err)
	}
	got, _ := FetchMetrics(context.Background(), start.Add(time.Minute), s, items, itemID, state, 1, f.fetch)
	if f.calls[1] != 1 || got[0] != "m1/1" {
		t.Errorf("unchanged state: calls %d, got %v, want no new request", f.calls[1], got)
	}

	status[1] = "running"
	got, _ = FetchMetrics(context.Background(), start.Add(2*time.Minute), s, items, itemID, state, 1, f.fetch)
	if f.calls[1] != 2 || got[0] != "m1/2" {
		t.Errorf("state changed: calls %d, got %v, want a new fetch", f.calls[1], got)
	}
}

// With no items left, a full fetch still forgets the old ones.
func TestFetchMetricsPrunesWithEmptyList(t *testing.T) {
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s := NewMetricsSchedule[string](time.Minute)
	f := newFakeFetch()
	f.fail[2] = errors.New("boom")
	_, _ = FetchMetrics(context.Background(), start, s, []item{{1}, {2}}, itemID, nil, 2, f.fetch)

	_, _ = FetchMetrics(context.Background(), start.Add(time.Minute), s, nil, itemID, nil, 2, f.fetch)
	if len(s.previous) != 0 || len(s.retries) != 0 || len(s.state) != 0 {
		t.Errorf("after an empty list: previous %v, retries %v, state %v, want all empty", s.previous, s.retries, s.state)
	}
}
