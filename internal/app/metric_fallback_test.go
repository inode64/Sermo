package app

import (
	"errors"
	"testing"
	"time"

	"sermo/internal/state"
	"sermo/internal/web"
)

type publishingMetricStore struct {
	batchPersistentMetricStore
	onSummary func()
}

func (s *publishingMetricStore) DaemonMetricSummary(string, time.Duration, time.Time) (state.MeasurementStat, error) {
	s.onSummary()
	return state.MeasurementStat{}, errors.New("store unavailable")
}

func (s *publishingMetricStore) ServiceMetricSummary(string, string, time.Duration, time.Time) (state.MeasurementStat, error) {
	s.onSummary()
	return state.MeasurementStat{}, errors.New("store unavailable")
}

func TestMetricFallbackKeepsObservationBoundary(t *testing.T) {
	at := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	t.Run("daemon", func(t *testing.T) {
		store := &publishingMetricStore{}
		sampler := &DaemonMetricSampler{store: store, samples: []daemonMetricSample{{at: at, rss: 100, rssOK: true}}}
		store.onSummary = func() {
			sampler.mu.Lock()
			defer sampler.mu.Unlock()
			sampler.samples = append(sampler.samples, daemonMetricSample{at: at.Add(time.Minute), rss: 900, rssOK: true})
		}
		got := sampler.Series(time.Hour)
		if got.Memory.Summary.Count != 1 || got.Memory.Summary.Avg != 100 || got.Current.At != at.Format(time.RFC3339) {
			t.Fatalf("fallback crossed observation boundary: %+v", got)
		}
	})
	t.Run("service", func(t *testing.T) {
		store := &publishingMetricStore{}
		sampler := NewServiceMetricSampler(store)
		current := web.ServiceRuntime{At: at.Format(time.RFC3339), RSS: 100, Count: 1}
		sampler.samples["web"] = []serviceMetricSample{{at: at, current: current}}
		store.onSummary = func() {
			sampler.mu.Lock()
			defer sampler.mu.Unlock()
			sampler.samples["web"] = append(sampler.samples["web"], serviceMetricSample{at: at.Add(time.Minute), current: web.ServiceRuntime{RSS: 900, Count: 1}})
		}
		got := sampler.Series("web", current, time.Hour, at)
		if got.Memory.Summary.Count != 1 || got.Memory.Summary.Avg != 100 || got.Current.At != current.At {
			t.Fatalf("fallback crossed observation boundary: %+v", got)
		}
	})
}
