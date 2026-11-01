package app

import (
	"errors"
	"testing"
	"time"

	"sermo/internal/process"
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

// History lives in the store, so memory holds only what the current reading and
// the shortest chart need when the store cannot be read. A year of per-cycle
// samples grew a long-running daemon by megabytes a day, per service.
func TestMetricSamplersBoundWhatTheyHoldInMemory(t *testing.T) {
	const cycle = 30 * time.Second
	start := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	const cycles = 3 * 120 // three hours
	limit := int(time.Hour/cycle) + 1
	last := start.Add((cycles - 1) * cycle)

	t.Run("daemon", func(t *testing.T) {
		now := start
		sampler := &DaemonMetricSampler{reader: &fakeDaemonMetricReader{rss: 1024, memTotal: 4096, numCPU: 1, clockTick: 100}, now: func() time.Time { return now }, pid: 42}
		for range cycles {
			sampler.sample()
			now = now.Add(cycle)
		}
		if got := len(sampler.samples); got > limit {
			t.Fatalf("daemon samples in memory = %d, want at most %d", got, limit)
		}
		if newest := sampler.samples[len(sampler.samples)-1]; !newest.at.Equal(last) {
			t.Fatalf("newest sample at %v, want %v", newest.at, last)
		}
	})
	t.Run("service", func(t *testing.T) {
		sampler := NewServiceMetricSampler(nil)
		for i := range cycles {
			sampler.record(t.Context(), "web", web.ServiceRuntime{Count: 1}, process.PrincipalIdentity{}, start.Add(time.Duration(i)*cycle))
		}
		if got := len(sampler.samples["web"]); got > limit {
			t.Fatalf("service samples in memory = %d, want at most %d", got, limit)
		}
		if newest, _, ok := sampler.latest("web"); !ok || !newest.at.Equal(last) {
			t.Fatalf("latest sample = %+v (%v), want one at %v", newest, ok, last)
		}
	})
}
