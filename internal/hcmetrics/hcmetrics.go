// Package hcmetrics contains helpers shared by the Hetzner sources: metric
// naming, API request timing and bounded concurrent fetching.
package hcmetrics

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Namespace is the prefix of every metric exposed by the exporter.
const Namespace = "hetzner_cloud"

// RequestTimeout bounds a single Hetzner API request.
const RequestTimeout = 10 * time.Second

// ListTimeout bounds listing a resource type, which can take several pages
// and retries.
const ListTimeout = 30 * time.Second

// MetricsWindow returns the time range for Hetzner metrics requests.
// Hetzner metrics have a 60s resolution. Asking for the last 5 minutes costs
// the same single request and still returns data when the newest datapoint
// is delayed; LastValue picks the newest valid point.
func MetricsWindow() (start, end time.Time, step int) {
	const (
		window     = 5 * time.Minute
		resolution = 60 * time.Second
	)
	end = time.Now().UTC()
	return end.Add(-window), end, int(resolution.Seconds())
}

// LastValue returns the newest valid value of a Hetzner metrics time series,
// skipping trailing values that are missing or NaN.
func LastValue[T any](series []T, getValue func(T) string) (float64, bool) {
	for i := len(series) - 1; i >= 0; i-- {
		val, err := strconv.ParseFloat(getValue(series[i]), 64)
		if err == nil && !math.IsNaN(val) {
			return val, true
		}
	}
	return 0, false
}

// Gibibyte is the size unit the Hetzner API calls "GB".
const Gibibyte = 1 << 30

// ParsePrice parses a Hetzner price string like "4.5100000000".
func ParsePrice(price string) (float64, bool) {
	value, err := strconv.ParseFloat(price, 64)
	return value, err == nil
}

// Timestamp converts t to Unix seconds for a metric; ok is false for a zero time.
func Timestamp(t time.Time) (float64, bool) {
	if t.IsZero() {
		return 0, false
	}
	return float64(t.Unix()), true
}

// BoolToFloat converts a boolean into a 1/0 metric value.
func BoolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// PartialError means a source fetched its resource list, but some per-item
// requests failed. The data is fresh, just incomplete.
type PartialError struct {
	Err error
}

func (e *PartialError) Error() string { return e.Err.Error() }
func (e *PartialError) Unwrap() error { return e.Err }

// IsPartial reports whether err is a PartialError.
func IsPartial(err error) bool {
	var partial *PartialError
	return errors.As(err, &partial)
}

// forEachLimited calls fn for indexes 0..n-1 with at most limit calls running
// concurrently. Once ctx is cancelled no further calls are started.
// It returns ctx.Err() if cancelled, otherwise an error summarising the failed
// calls, if any.
func forEachLimited(ctx context.Context, n, limit int, fn func(i int) error) error {
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	sem := make(chan struct{}, max(limit, 1))

loop:
	for i := range n {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break loop
		}
		if ctx.Err() != nil {
			<-sem
			break
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := fn(i); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return err
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%d of %d requests failed, first error: %w", len(errs), n, errs[0])
}

// fetchEach calls fetch for every item, at most limit at a time and each with
// RequestTimeout, and returns the results and errors in item order. Items not
// started because ctx ended get ctx.Err().
func fetchEach[T, R any](ctx context.Context, items []T, limit int, fetch func(context.Context, T) (R, error)) ([]R, []error) {
	results := make([]R, len(items))
	errs := make([]error, len(items))
	started := make([]bool, len(items))
	_ = forEachLimited(ctx, len(items), limit, func(i int) error {
		started[i] = true
		itemCtx, cancel := context.WithTimeout(ctx, RequestTimeout)
		defer cancel()

		results[i], errs[i] = fetch(itemCtx, items[i])
		return errs[i]
	})
	for i := range items {
		if !started[i] {
			errs[i] = ctx.Err()
		}
	}
	return results, errs
}

// DescSet creates the metric descriptors of one subsystem and remembers them,
// so a source's Describe always matches the metrics it declares.
type DescSet struct {
	subsystem     string
	resourceLabel string
	descs         []*prometheus.Desc
}

// NewDescSet returns a DescSet for metrics named hetzner_cloud_<subsystem>_* whose
// first label is resourceLabel (e.g. "server").
func NewDescSet(subsystem, resourceLabel string) *DescSet {
	return &DescSet{subsystem: subsystem, resourceLabel: resourceLabel}
}

// New creates and registers a descriptor. The resource label comes first,
// followed by labels.
func (d *DescSet) New(name, help string, labels ...string) *prometheus.Desc {
	desc := prometheus.NewDesc(
		prometheus.BuildFQName(Namespace, d.subsystem, name),
		help, append([]string{d.resourceLabel}, labels...), nil)
	d.descs = append(d.descs, desc)
	return desc
}

// Describe sends every descriptor created with New.
func (d *DescSet) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range d.descs {
		ch <- desc
	}
}
