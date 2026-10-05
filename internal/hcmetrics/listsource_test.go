package hcmetrics

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestListSourceKeepsDataOnFailure(t *testing.T) {
	descs := NewDescSet("thing", "thing")
	desc := descs.New("info", "help")
	items, fail := []string{"a", "b"}, false
	source := NewListSource("things", descs,
		func(context.Context) ([]string, error) {
			if fail {
				return nil, errors.New("boom")
			}
			return items, nil
		},
		func(ch chan<- prometheus.Metric, item string) {
			ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, 1, item)
		})

	if err := source.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	fail = true
	if err := source.Fetch(context.Background()); err == nil {
		t.Fatal("want error from failing list")
	}
	if got := testutil.CollectAndCount(source); got != 2 {
		t.Errorf("series after failed list = %d, want previous 2", got)
	}
}
