package hcmetrics

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestForEachLimited(t *testing.T) {
	const n, limit = 9, 3
	var calls, inFlight, maxInFlight atomic.Int32

	// All `limit` calls must be in flight at the same time to pass the
	// barrier, which proves the calls really run concurrently.
	var barrier sync.WaitGroup
	barrier.Add(limit)

	err := forEachLimited(context.Background(), n, limit, func(i int) error {
		calls.Add(1)
		cur := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			m := maxInFlight.Load()
			if cur <= m || maxInFlight.CompareAndSwap(m, cur) {
				break
			}
		}
		if i < limit {
			barrier.Done()
			barrier.Wait()
		}
		if i%4 == 0 {
			return errors.New("boom")
		}
		return nil
	})

	if calls.Load() != n {
		t.Errorf("calls = %d, want %d", calls.Load(), n)
	}
	if maxInFlight.Load() != limit {
		t.Errorf("max concurrent calls = %d, want %d", maxInFlight.Load(), limit)
	}
	if err == nil || err.Error() != "3 of 9 requests failed, first error: boom" {
		t.Errorf("err = %v", err)
	}
}

func TestForEachLimitedStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32

	err := forEachLimited(ctx, 100, 2, func(i int) error {
		if calls.Add(1) == 2 {
			cancel()
		}
		time.Sleep(time.Millisecond)
		return nil
	})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if got := calls.Load(); got > 4 {
		t.Errorf("calls after cancel = %d, want no new calls started", got)
	}
}

func TestLastValue(t *testing.T) {
	get := func(s string) string { return s }

	tests := []struct {
		name   string
		series []string
		want   float64
		wantOK bool
	}{
		{"newest value", []string{"1", "2.5"}, 2.5, true},
		{"skips trailing NaN", []string{"1", "2", "NaN"}, 2, true},
		{"skips trailing invalid", []string{"3", "oops"}, 3, true},
		{"empty", nil, 0, false},
		{"only NaN", []string{"NaN"}, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := LastValue(tt.series, get)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("LastValue = %v, %v; want %v, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestDescSetDescribesEveryDesc(t *testing.T) {
	set := NewDescSet("test", "resource")
	a := set.New("a", "help a")
	b := set.New("b", "help b", "extra")

	ch := make(chan *prometheus.Desc, 10)
	set.Describe(ch)
	close(ch)

	var got []*prometheus.Desc
	for desc := range ch {
		got = append(got, desc)
	}
	if len(got) != 2 || got[0] != a || got[1] != b {
		t.Errorf("Describe returned %d descs, want a and b in order", len(got))
	}
}
