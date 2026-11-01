package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sermo/internal/execx/execxtest"
	"sermo/internal/metrics"
	"sermo/internal/servicemgr"
)

// hungReadBudget bounds what must not wait for a blocked read. Far above any
// give-up in these tests, so only a real wedge exceeds it.
const hungReadBudget = 5 * time.Second

// hungRead stands in for a read on a hard network mount whose server is gone:
// it blocks with no context to reach it until the test releases it.
type hungRead struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
	enter   sync.Once
	free    sync.Once
}

func newHungRead(t *testing.T) *hungRead {
	t.Helper()
	h := &hungRead{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(h.unblock)
	return h
}

func (h *hungRead) block() {
	h.calls.Add(1)
	h.enter.Do(func() { close(h.entered) })
	<-h.release
}

func (h *hungRead) unblock() { h.free.Do(func() { close(h.release) }) }

func awaitWithin(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(hungReadBudget):
		t.Fatalf("%s did not finish within %s", what, hungReadBudget)
	}
}

// assertMonitorSurvives runs stuck, whose cycle hits hung, beside a healthy
// target: the healthy one keeps cycling, and a reload and a shutdown both finish
// while the read stays blocked.
func assertMonitorSurvives(t *testing.T, hung *hungRead, stuck *Watch) {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"services", "run"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	enabled := filepath.Join(dir, "services")
	global := filepath.Join(dir, "sermo.yml")
	body := fmt.Sprintf("engine:\n  runtime: %q\n  interval: 100ms\npaths:\n  services: [%q]\n\ndefaults:\n  policy: { cooldown: 1m }\n", filepath.Join(dir, "run"), enabled)
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	service := "name: web\nchecks:\n  ping:\n    type: command\n    command: [\"/bin/true\"]\n"
	if err := os.WriteFile(filepath.Join(enabled, "web.yml"), []byte(service), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := loadTestConfig(t, global)
	ready := NewReadiness(string(servicemgr.BackendSystemd), 1, 0)
	deps := Deps{Interval: 100 * time.Millisecond, Now: time.Now, Emit: func(Event) {}, ExecxRunner: &execxtest.Runner{}, Settling: NewSettling(ready)}
	collector := metrics.New(fakeSwapReader{})
	workers, _, _ := BuildWorkers(t.Context(), cfg, deps, collector)
	forceWorkerBackendActive(workers)

	var healthy atomic.Int64
	other := &Watch{Name: "other", Cycle: func(context.Context) { healthy.Add(1) }}
	mon := NewMonitor(cfg, deps, Scheduler{Interval: 5 * time.Millisecond}, ready, collector, nil, workers, []*Watch{stuck, other})
	mon.ConfigPath = global
	hermeticMonitor(t, mon)
	mon.Logger = slog.New(slog.DiscardHandler)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		mon.Run(ctx)
	}()
	reloadDone := make(chan struct{})
	t.Cleanup(func() {
		// A wedge leaves these running: free the read, then let them drain.
		hung.unblock()
		cancel()
		<-runDone
	})

	awaitWithin(t, hung.entered, "the blocked read")
	before := healthy.Load()
	deadline := time.Now().Add(hungReadBudget)
	for healthy.Load() < before+3 {
		if time.Now().After(deadline) {
			t.Fatal("a blocked read stopped another target's cycles")
		}
		time.Sleep(time.Millisecond)
	}

	go func() {
		defer close(reloadDone)
		mon.Reload(ctx)
	}()
	awaitWithin(t, reloadDone, "a reload")

	cancel()
	awaitWithin(t, runDone, "a shutdown")
}
