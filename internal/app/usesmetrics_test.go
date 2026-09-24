package app

import (
	"sermo/internal/execx/execxtest"
	"sermo/internal/servicemgr"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sermo/internal/process"
)

func TestUsesMetrics(t *testing.T) {
	cases := []struct {
		name        string
		tree        map[string]any
		wantService bool
		wantSystem  bool
	}{
		{
			name:        "service-scope metric check",
			tree:        map[string]any{"checks": map[string]any{"c": map[string]any{"type": "metric", "name": "cpu"}}},
			wantService: true,
		},
		{
			name:        "metric check without scope defaults to service",
			tree:        map[string]any{"checks": map[string]any{"c": map[string]any{"type": "metric"}}},
			wantService: true,
		},
		{
			name: "system metric nested in a rule and-condition",
			tree: map[string]any{"rules": map[string]any{"r": map[string]any{"if": map[string]any{
				"and": []any{map[string]any{"metric": map[string]any{"scope": "system", "name": "load1"}}},
			}}}},
			wantSystem: true,
		},
		{
			name: "system metric under a not-condition",
			tree: map[string]any{"rules": map[string]any{"r": map[string]any{"if": map[string]any{
				"not": map[string]any{"metric": map[string]any{"scope": "system", "name": "load1"}},
			}}}},
			wantSystem: true,
		},
		{
			name: "system metric inline under failed probe",
			tree: map[string]any{"rules": map[string]any{"r": map[string]any{"if": map[string]any{
				"failed": map[string]any{"metric": map[string]any{"scope": "system", "name": "total_cpu"}},
			}}}},
			wantSystem: true,
		},
		{
			name: "service metric inline under active probe",
			tree: map[string]any{"rules": map[string]any{"r": map[string]any{"if": map[string]any{
				"active": map[string]any{"metric": map[string]any{"name": "memory"}},
			}}}},
			wantService: true,
		},
		{
			name: "no metric references",
			tree: map[string]any{"checks": map[string]any{"c": map[string]any{"type": "tcp"}}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, sys := usesMetrics(c.tree)
			if svc != c.wantService || sys != c.wantSystem {
				t.Fatalf("usesMetrics = (service=%v, system=%v), want (%v, %v)", svc, sys, c.wantService, c.wantSystem)
			}
		})
	}
}

func TestCycleProcessSourceCachesWithinCycle(t *testing.T) {
	cycle := 1
	calls := 0
	source := cycleProcessSource(func() []process.Process {
		calls++
		return []process.Process{{PID: calls}}
	}, func() int {
		return cycle
	})

	first := source()
	second := source()
	if calls != 1 {
		t.Fatalf("same cycle discoveries = %d, want 1", calls)
	}
	if first[0].PID != 1 || second[0].PID != 1 {
		t.Fatalf("same cycle processes = %v then %v, want cached PID 1", first, second)
	}

	cycle = 2
	third := source()
	if calls != 2 {
		t.Fatalf("next cycle discoveries = %d, want 2", calls)
	}
	if third[0].PID != 2 {
		t.Fatalf("next cycle processes = %v, want PID 2", third)
	}
}

func TestWorkerProcessCountsReuseConcurrentCycleDiscovery(t *testing.T) {
	var backendCalls atomic.Int32
	reads := 0
	tree := map[string]any{"processes": map[string]any{
		"main": map[string]any{"type": "command_match", "exe": "/opt/app/main", "user": "500"},
	}}
	w, _, warnings := buildWorker(t.Context(), "svc", "svc.service", tree, Deps{
		Backend: servicemgr.BackendSystemd, Manager: fakeManager{},
		Runtime: t.TempDir(), DefaultTimeout: time.Second,
		ProcReader: process.NewCachingReader(countingProcReader{calls: &reads}, time.Minute), ExecxRunner: &execxtest.Runner{},
		BackendPIDs: func() []int { backendCalls.Add(1); return []int{100} },
	}, nil)
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	baseline := backendCalls.Load()
	for cycle := 1; cycle <= 2; cycle++ {
		var wg sync.WaitGroup
		for range 10 {
			wg.Go(func() {
				if got := w.CheckDeps.ProcessCount("", "", ""); got != 0 {
					t.Errorf("count = %d", got)
				}
				w.CheckDeps.Strays()
				w.CheckDeps.StaleBinaries()
			})
		}
		wg.Wait()
		if got := backendCalls.Load() - baseline; got != int32(cycle) {
			t.Fatalf("backend queries = %d, want %d", got, cycle)
		}
		w.cycle++
	}
}
