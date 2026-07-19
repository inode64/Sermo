package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/metrics"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
)

func TestMonitorReloadPreservesWorkerState(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"services", "run"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	enabled := filepath.Join(dir, "services")

	baseCfg := fmt.Sprintf(`engine:
  interval: 100ms
paths:
  services: [%[1]q]
  runtime: %[2]q
defaults:
  policy: { cooldown: 1m }
`, enabled, filepath.Join(dir, "run"))

	global := filepath.Join(dir, "sermo.yml")
	service := func(name string) string {
		return fmt.Sprintf(`
name: %s
checks:
  ping:
    type: command
    command: ["/bin/true"]
`, name)
	}
	if err := os.WriteFile(global, []byte(baseCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(enabled, "web.yml"), []byte(service("web")), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(global)
	if err != nil {
		t.Fatal(err)
	}
	ready := NewReadiness(string(servicemgr.BackendSystemd), 1, 0)
	deps := Deps{Interval: 100 * time.Millisecond, Now: time.Now, Emit: func(Event) {}, ExecxRunner: execx.CommandRunner{}, Settling: NewSettling(ready)}
	collector := metrics.New(metrics.OSReader{})
	workers, _, _ := BuildWorkers(t.Context(), cfg, deps, collector)
	if len(workers) != 1 {
		t.Fatalf("workers = %d, want 1", len(workers))
	}
	forceWorkerBackendActive(workers)

	t0 := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	workers[0].cycle = 11
	workers[0].State = &rules.RemediationState{
		LastActionAt:   t0,
		RecentActions:  []time.Time{t0, t0.Add(5 * time.Second)},
		CurrentBackoff: 42 * time.Second,
	}
	workers[0].libBaseline = map[string]string{
		"/etc/web.conf": "123:456789",
	}

	mon := NewMonitor(cfg, deps, Scheduler{Interval: 20 * time.Millisecond}, ready, collector, nil)
	mon.ConfigPath = global
	mon.Logger = slog.Default()
	mon.Init(workers, nil)

	ctx := t.Context()
	go mon.Run(ctx)
	waitReady(t, ready)

	if err := os.WriteFile(filepath.Join(enabled, "web.yml"), []byte(service("web")), 0o644); err != nil {
		t.Fatal(err)
	}
	mon.Reload(ctx)

	// Stop the generation so we can safely observe the (possibly new) worker's
	// internal mutable state without racing the scheduler cyclers. Restart
	// afterwards so readiness/shutdown behavior remains realistic for the test.
	mon.stopGenerationLocked(false)
	mon.mu.Lock()
	w := mon.workers[0]
	cycle := w.cycle
	state := w.State
	baseline := w.libBaseline
	mon.mu.Unlock()
	mon.startGenerationLocked(ctx, false)

	if cycle < 11 {
		t.Fatalf("cycle after reload = %d, want preserved >= 11", cycle)
	}
	// RemediationState is transferred by capture/apply during reload.
	// Recover() (called on healthy cycles with no firing remediation rules) clears
	// CurrentBackoff, so we assert the fields that are stable across a post-reload
	// healthy cycle. The pure unit test TestCaptureAndApplyWorkerState covers the
	// full struct copy including backoff.
	if state == nil || !state.LastActionAt.Equal(t0) || len(state.RecentActions) != 2 {
		t.Fatalf("remediation state after reload = %+v, want preserved LastActionAt=%v and 2 recent actions", state, t0)
	}
	if got := baseline["/etc/web.conf"]; got != "123:456789" {
		t.Fatalf("libBaseline after reload = %v, want preserved", baseline)
	}
}

func TestMonitorReloadRejectsInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"catalog", "services", "run"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	enabled := filepath.Join(dir, "services")
	global := filepath.Join(dir, "sermo.yml")
	valid := fmt.Sprintf(`engine:
  interval: 100ms
paths:
  services: [%q]
  runtime: %q
defaults:
  policy: { cooldown: 1m }
`, enabled, filepath.Join(dir, "run"))
	if err := os.WriteFile(global, []byte(valid), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(enabled, "web.yml"), []byte(`
name: web
checks:
  ping:
    type: command
    command: ["/bin/true"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(global)
	if err != nil {
		t.Fatal(err)
	}
	ready := NewReadiness(string(servicemgr.BackendSystemd), 1, 0)
	deps := Deps{Interval: 100 * time.Millisecond, Now: time.Now, Emit: func(Event) {}, ExecxRunner: execx.CommandRunner{}, Settling: NewSettling(ready)}
	collector := metrics.New(metrics.OSReader{})
	workers, _, _ := BuildWorkers(t.Context(), cfg, deps, collector)
	forceWorkerBackendActive(workers)
	const seededCycle = 5
	workers[0].cycle = seededCycle

	mon := NewMonitor(cfg, deps, Scheduler{Interval: 20 * time.Millisecond}, ready, collector, nil)
	mon.ConfigPath = global
	mon.Logger = slog.Default()
	mon.Init(workers, nil)

	ctx := t.Context()
	go mon.Run(ctx)
	waitReady(t, ready)

	invalid := fmt.Sprintf(`engine:
  interval: notaduration
paths:
  services: [%q]
  runtime: %q
defaults:
  policy: { cooldown: 1m }
`, enabled, filepath.Join(dir, "run"))
	if err := os.WriteFile(global, []byte(invalid), 0o644); err != nil {
		t.Fatal(err)
	}
	mon.Reload(ctx)

	// Stop briefly for a race-free observation of live worker state: reading
	// worker.cycle while the generation runs would race the cycle increment.
	// The reject path does not replace workers, so the seeded generation keeps
	// running — a replaced generation would reset cycle below the seeded value.
	mon.stopGenerationLocked(false)
	mon.mu.Lock()
	after := mon.workers[0].cycle
	mon.mu.Unlock()
	mon.startGenerationLocked(ctx, false)

	if after < seededCycle {
		t.Fatalf("cycle after rejected reload = %d, want the seeded generation preserved (>= %d)", after, seededCycle)
	}
	if rep := ready.Report(context.Background()); !rep.Ready || rep.Status != TargetStateOK {
		t.Fatalf("readiness = %+v, want ready during rejected reload", rep)
	}
}

func TestReloadConfigCompatibilityRejectsProcessLifetimeChanges(t *testing.T) {
	current := reloadCompatibilityConfig("/run/sermo", "/var/lib/sermo", "current", 2)
	tests := []struct {
		name string
		next *config.Config
		want string
	}{
		{
			name: "unchanged paths",
			next: reloadCompatibilityConfig("/run/sermo", "/var/lib/sermo", "current", 2),
		},
		{
			name: "runtime changed",
			next: reloadCompatibilityConfig("/run/sermo-next", "/var/lib/sermo", "current", 2),
			want: "paths.runtime changed; restart sermod",
		},
		{
			name: "state changed",
			next: reloadCompatibilityConfig("/run/sermo", "/var/lib/sermo-next", "current", 2),
			want: "paths.state changed; restart sermod",
		},
		{
			name: "web auth changed",
			next: reloadCompatibilityConfig("/run/sermo", "/var/lib/sermo", "next", 2),
			want: "web configuration changed; restart sermod",
		},
		{
			name: "operation slots changed",
			next: reloadCompatibilityConfig("/run/sermo", "/var/lib/sermo", "current", 4),
			want: "engine.max_parallel_operations changed; restart sermod",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reloadConfigCompatibilityError(current, tt.next); got != tt.want {
				t.Fatalf("reloadConfigCompatibilityError() = %q, want %q", got, tt.want)
			}
		})
	}
}

func reloadCompatibilityConfig(runtime, stateDir, password string, operationSlots int) *config.Config {
	return &config.Config{Global: config.Global{
		Runtime: runtime, State: stateDir,
		Raw: map[string]any{
			config.SectionEngine: map[string]any{config.EngineKeyMaxParallelOperations: operationSlots},
			config.SectionWeb:    map[string]any{config.WebKeyPort: 9797, config.WebKeyPassword: password},
		},
	}}
}

func TestMonitorApplyConfigUpdatesSchedulerInterval(t *testing.T) {
	current := reloadCompatibilityConfig("/run/sermo", "/var/lib/sermo", "current", 2)
	next := reloadCompatibilityConfig("/run/sermo", "/var/lib/sermo", "current", 2)
	next.Global.Raw[config.SectionEngine] = map[string]any{
		config.EntryKeyInterval:               "10s",
		config.EngineKeyMaxParallelOperations: 2,
	}
	mon := NewMonitor(current, Deps{Interval: time.Minute}, Scheduler{Interval: time.Minute, OpSlots: 2}, nil, nil, nil)
	mon.Logger = slog.Default()

	mon.applyConfig(next)

	if mon.scheduler.Interval != 10*time.Second {
		t.Fatalf("scheduler interval = %s, want 10s", mon.scheduler.Interval)
	}
}

func TestMonitorGenerationRunsDaemonMetricSampler(t *testing.T) {
	reader := &signalingDaemonMetricReader{
		fakeDaemonMetricReader: &fakeDaemonMetricReader{rss: 1024, memTotal: 4096, numCPU: 1, clockTick: 100},
		sampled:                make(chan struct{}),
	}
	sampler := &DaemonMetricSampler{reader: reader, now: time.Now, pid: 42}
	mon := NewMonitor(&config.Config{}, Deps{DaemonMetricSampler: sampler, Interval: time.Hour}, Scheduler{Interval: time.Hour}, nil, nil, nil)
	mon.Init(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		mon.Run(ctx)
		close(done)
	}()

	select {
	case <-reader.sampled:
	case <-time.After(time.Second):
		t.Fatal("monitor did not start the daemon metric sampler")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("monitor did not stop the daemon metric sampler")
	}
}

func TestMonitorReloadEmptyFleetRestoresSchedulerAndCollector(t *testing.T) {
	dir := t.TempDir()
	servicesDir := filepath.Join(dir, "services")
	emptyServicesDir := filepath.Join(dir, "empty-services")
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(emptyServicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(dir, "sermo.yml")
	runtimeDir := filepath.Join(dir, "run")
	initial := fmt.Sprintf(`engine:
  interval: 100ms
paths:
  services: [%q]
  runtime: %q
defaults:
  policy: { cooldown: 1m }
`, servicesDir, runtimeDir)
	if err := os.WriteFile(global, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(servicesDir, "web.yml"), []byte(`
name: web
checks:
  ping:
    type: command
    command: ["/bin/true"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(global)
	if err != nil {
		t.Fatal(err)
	}
	ready := NewReadiness(string(servicemgr.BackendSystemd), 1, 0)
	deps := Deps{Interval: 100 * time.Millisecond, SystemFreshness: 50 * time.Millisecond, Now: time.Now, Emit: func(Event) {}, ExecxRunner: execx.CommandRunner{}, Settling: NewSettling(ready)}
	collector := metrics.New(metrics.OSReader{})
	collector.SystemFreshness = deps.SystemFreshness
	workers, _, _ := BuildWorkers(t.Context(), cfg, deps, collector)
	forceWorkerBackendActive(workers)
	mon := NewMonitor(cfg, deps, Scheduler{Interval: deps.Interval}, ready, collector, nil)
	mon.ConfigPath = global
	mon.Logger = slog.Default()
	mon.Init(workers, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		mon.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	waitReady(t, ready)

	empty := fmt.Sprintf(`engine:
  interval: 10s
paths:
  services: [%q]
  runtime: %q
defaults:
  policy: { cooldown: 1m }
`, emptyServicesDir, runtimeDir)
	if err := os.WriteFile(global, []byte(empty), 0o644); err != nil {
		t.Fatal(err)
	}
	mon.Reload(ctx)

	mon.mu.Lock()
	gotInterval := mon.scheduler.Interval
	gotFreshness := mon.deps.SystemFreshness
	mon.mu.Unlock()
	if gotInterval != 100*time.Millisecond || gotFreshness != 50*time.Millisecond {
		t.Fatalf("rollback scheduler/freshness = %s/%s, want 100ms/50ms", gotInterval, gotFreshness)
	}
	if collector.SystemFreshness != 50*time.Millisecond {
		t.Fatalf("collector freshness = %s, want 50ms", collector.SystemFreshness)
	}
}

func forceWorkerBackendActive(workers []*Worker) {
	for _, w := range workers {
		if w == nil {
			continue
		}
		w.CheckDeps.Status = func(context.Context) (servicemgr.Status, error) {
			return servicemgr.StatusActive, nil
		}
	}
}

func waitReady(t *testing.T, ready *Readiness) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if rep := ready.Report(context.Background()); rep.Ready {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("readiness not ready in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
