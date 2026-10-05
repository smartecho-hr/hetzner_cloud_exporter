package hcmetrics

import (
	"context"
	"fmt"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// ListSource is a Source for resources that need a single list request per
// poll (volumes, IPs, SSH keys, ...). Collect calls collect for every item of
// the last successful list; a failed list keeps the previous items.
type ListSource[T any] struct {
	name    string
	descs   *DescSet
	list    func(ctx context.Context) ([]T, error)
	collect func(ch chan<- prometheus.Metric, item T)

	mu    sync.RWMutex
	items []T
}

// NewListSource returns a ListSource. name is the source and collector name,
// e.g. "volumes".
func NewListSource[T any](
	name string,
	descs *DescSet,
	list func(ctx context.Context) ([]T, error),
	collect func(ch chan<- prometheus.Metric, item T),
) *ListSource[T] {
	return &ListSource[T]{name: name, descs: descs, list: list, collect: collect}
}

// Name implements poller.Source.
func (s *ListSource[T]) Name() string { return s.name }

// Fetch implements poller.Source: it lists the resources and stores them;
// on failure the previous list is kept.
func (s *ListSource[T]) Fetch(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, ListTimeout)
	defer cancel()

	items, err := s.list(ctx)
	if err != nil {
		return fmt.Errorf("listing %s: %w", s.name, err)
	}

	s.mu.Lock()
	s.items = items
	s.mu.Unlock()
	return nil
}

// Items returns the items of the last successful list.
func (s *ListSource[T]) Items() []T {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.items
}

// Describe implements prometheus.Collector.
func (s *ListSource[T]) Describe(ch chan<- *prometheus.Desc) { s.descs.Describe(ch) }

// Collect implements prometheus.Collector from the stored list; it never
// calls the API.
func (s *ListSource[T]) Collect(ch chan<- prometheus.Metric) {
	for _, item := range s.Items() {
		s.collect(ch, item)
	}
}
