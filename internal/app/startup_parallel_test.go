package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/metrics"
	"sermo/internal/servicemgr"
)

// rendezvousRunner answers init-backend queries like procInfoRunner but holds
// each call until a second one is in flight (or a short deadline passes), so a
// caller that wires services one after another can never overlap two calls
// while a parallel caller shows the overlap in maxInFlight.
type rendezvousRunner struct {
	inner    execx.Runner
	inFlight atomic.Int32
	max      atomic.Int32
	mu       sync.Mutex
	cond     *sync.Cond
}

func newRendezvousRunner(inner execx.Runner) *rendezvousRunner {
	r := &rendezvousRunner{inner: inner}
	r.cond = sync.NewCond(&r.mu)
	return r
}

func (r *rendezvousRunner) Run(ctx context.Context, name string, args ...string) (execx.Result, error) {
	n := r.inFlight.Add(1)
	for {
		m := r.max.Load()
		if n <= m || r.max.CompareAndSwap(m, n) {
			break
		}
	}
	r.mu.Lock()
	r.cond.Broadcast()
	deadline := time.Now().Add(150 * time.Millisecond)
	for r.inFlight.Load() < 2 && time.Now().Before(deadline) {
		r.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		r.mu.Lock()
	}
	r.mu.Unlock()
	defer r.inFlight.Add(-1)
	return r.inner.Run(ctx, name, args...)
}

func writeParallelServicesConfig(t *testing.T, n int) *config.Config {
	t.Helper()
	root := t.TempDir()
	enabled := filepath.Join(root, "services")
	if err := os.MkdirAll(enabled, 0o755); err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(globalPath, []byte("paths:\n  services: ["+enabled+"]\ndefaults:\n  policy:\n    cooldown: 5m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		body := fmt.Sprintf("name: svc-%02d\nservice: svc-%02d\nchecks:\n  state:\n    type: service\n    expect: active\n", i, i)
		if err := os.WriteFile(filepath.Join(enabled, fmt.Sprintf("svc-%02d.yml", i)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(globalPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

func parallelStartupDeps(t *testing.T, runner execx.Runner) (Deps, *metrics.Collector) {
	t.Helper()
	collector := metrics.New(metrics.OSReader{})
	return Deps{
		Backend:          servicemgr.BackendSystemd,
		Manager:          fakeManager{},
		Runtime:          t.TempDir(),
		DefaultTimeout:   time.Second,
		OperationTimeout: time.Second,
		MaxParallel:      4,
		Live:             NewLiveMetrics(),
		LiveCollector:    collector,
		ServiceMetrics:   NewServiceMetricSampler(),
		Collector:        collector,
		ExecxRunner:      runner,
		Now:              time.Now,
		Emit:             func(Event) {},
	}, collector
}

// A loaded VM host took ten minutes to reach its web listener because 49
// services were wired one after another, each waiting on the init backend.
// Services are independent until the cascade is wired, so they are wired side
// by side — and still come back in name order.
func TestBuildWorkersWiresServicesInParallel(t *testing.T) {
	cfg := writeParallelServicesConfig(t, 6)
	runner := newRendezvousRunner(procInfoRunner{})
	deps, collector := parallelStartupDeps(t, runner)
	workers, _, warnings := BuildWorkers(t.Context(), cfg, deps, collector)
	if len(warnings) != 0 {
		t.Fatalf("BuildWorkers warnings = %v, want none", warnings)
	}
	if runner.max.Load() < 2 {
		t.Fatalf("max in-flight init queries = %d, want services wired side by side", runner.max.Load())
	}
	if len(workers) != 6 {
		t.Fatalf("workers = %d, want 6", len(workers))
	}
	for i, w := range workers {
		if want := fmt.Sprintf("svc-%02d", i); w.Service != want {
			t.Fatalf("workers[%d] = %s, want %s (name order kept)", i, w.Service, want)
		}
	}
}

// The web backend prepares its services the same way and registers them in
// name order once every one is prepared.
func TestNewWebBackendPreparesServicesInParallel(t *testing.T) {
	cfg := writeParallelServicesConfig(t, 6)
	runner := newRendezvousRunner(procInfoRunner{})
	deps, _ := parallelStartupDeps(t, runner)
	deps.Snapshots = NewSnapshots()
	deps.Observability = NewObservabilityRegistry()
	wb, warnings := NewWebBackend(t.Context(), cfg, deps)
	if len(warnings) != 0 {
		t.Fatalf("NewWebBackend warnings = %v, want none", warnings)
	}
	if runner.max.Load() < 2 {
		t.Fatalf("max in-flight init queries = %d, want services prepared side by side", runner.max.Load())
	}
	if len(wb.order) != 6 {
		t.Fatalf("registered services = %v, want 6", wb.order)
	}
	for i, name := range wb.order {
		if want := fmt.Sprintf("svc-%02d", i); name != want || wb.entries[name] == nil {
			t.Fatalf("order[%d] = %s, want %s with an entry", i, name, want)
		}
	}
}

func TestForEachParallelVisitsEveryIndexOnce(t *testing.T) {
	var visits [7]atomic.Int32
	forEachParallel(len(visits), 0, func(i int) { visits[i].Add(1) })
	for i := range visits {
		if visits[i].Load() != 1 {
			t.Fatalf("index %d visited %d times", i, visits[i].Load())
		}
	}
}
