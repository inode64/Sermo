package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"sermo/internal/state"
	"sermo/internal/web"
)

func TestServiceMetricSamplerReadsPersistedHistory(t *testing.T) {
	store, err := state.OpenContext(context.Background(), filepath.Join(t.TempDir(), state.Filename))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	base := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	first := NewServiceMetricSampler(store)
	first.Record("web", web.ServiceRuntime{
		At:            base.UTC().Format(time.RFC3339),
		ProcessTotals: web.ProcessTotals{Count: 1, RSS: 1024, IORead: 1000, IOWrite: 2000, CPU: 10, HasCPU: true},
	})
	first.Record("web", web.ServiceRuntime{
		At:            base.Add(time.Minute).UTC().Format(time.RFC3339),
		ProcessTotals: web.ProcessTotals{Count: 1, RSS: 2048, IORead: 7000, IOWrite: 5000, CPU: 20, HasCPU: true},
	})

	second := NewServiceMetricSampler(store)
	afterRestart := second.Series("web", web.ServiceRuntime{
		At:            base.Add(2 * time.Minute).UTC().Format(time.RFC3339),
		ProcessTotals: web.ProcessTotals{Count: 1, RSS: 4096, IORead: 9000, IOWrite: 7000},
	}, time.Hour)

	if afterRestart.CPU.Summary.Count != 2 || len(afterRestart.CPU.Points) == 0 {
		t.Fatalf("persisted CPU series not restored: summary=%+v points=%+v", afterRestart.CPU.Summary, afterRestart.CPU.Points)
	}
	if afterRestart.IO.Summary.Count != 1 || len(afterRestart.IO.Points) == 0 {
		t.Fatalf("persisted IO series not restored: summary=%+v points=%+v", afterRestart.IO.Summary, afterRestart.IO.Points)
	}
	if afterRestart.Memory.Summary.Count != 2 || len(afterRestart.Memory.Points) == 0 {
		t.Fatalf("persisted memory series not restored: summary=%+v points=%+v", afterRestart.Memory.Summary, afterRestart.Memory.Points)
	}
	if afterRestart.Current.IOReady {
		t.Fatalf("fresh sampler current IO should be measuring, got %+v", afterRestart.Current)
	}
}

func TestServiceMetricSamplerSeriesDoesNotRecordDashboardReads(t *testing.T) {
	base := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	sampler := NewServiceMetricSampler()
	sampler.Record("web", web.ServiceRuntime{
		At:            base.UTC().Format(time.RFC3339),
		ProcessTotals: web.ProcessTotals{Count: 1, RSS: 1024, CPU: 10, HasCPU: true},
	})
	current := web.ServiceRuntime{
		At:            base.Add(time.Minute).UTC().Format(time.RFC3339),
		ProcessTotals: web.ProcessTotals{Count: 1, RSS: 4096, CPU: 40, HasCPU: true},
	}

	first := sampler.Series("web", current, time.Hour)
	second := sampler.Series("web", current, time.Hour)
	if first.Memory.Summary.Count != 1 || second.Memory.Summary.Count != 1 {
		t.Fatalf("dashboard reads changed memory samples: first=%d second=%d", first.Memory.Summary.Count, second.Memory.Summary.Count)
	}
	if first.CPU.Summary.Avg != 10 || second.CPU.Summary.Avg != 10 {
		t.Fatalf("dashboard current value changed CPU history: first=%v second=%v", first.CPU.Summary.Avg, second.CPU.Summary.Avg)
	}
	if second.Current.RSS != current.RSS || second.Current.CPU != current.CPU {
		t.Fatalf("current runtime = %+v, want %+v", second.Current, current)
	}
}
