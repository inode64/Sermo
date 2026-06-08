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
	"sermo/internal/metrics"
	"sermo/internal/rules"
)

func TestMonitorReloadPreservesWorkerState(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"profiles", "enabled", "run"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	enabled := filepath.Join(dir, "enabled")

	baseCfg := fmt.Sprintf(`engine:
  interval: 100ms
paths:
  profiles: [%[1]q]
  enabled: [%[2]q]
  runtime: %[3]q
defaults:
  policy: { cooldown: 1m }
`, filepath.Join(dir, "profiles"), enabled, filepath.Join(dir, "run"))

	global := filepath.Join(dir, "sermo.yml")
	service := func(name string) string {
		return fmt.Sprintf(`kind: service
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
	deps := Deps{Interval: 100 * time.Millisecond, Now: time.Now, Emit: func(Event) {}}
	collector := metrics.New(metrics.OSReader{})
	workers, _ := BuildWorkers(cfg, deps, collector)
	if len(workers) != 1 {
		t.Fatalf("workers = %d, want 1", len(workers))
	}
	workers[0].cycle = 11
	workers[0].State = &rules.RemediationState{LastActionAt: time.Now()}

	ready := NewReadiness("systemd", 1, 0)
	mon := NewMonitor(cfg, deps, Scheduler{Interval: 20 * time.Millisecond}, ready, collector, nil)
	mon.ConfigPath = global
	mon.Logger = slog.Default()
	mon.Init(workers, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mon.Run(ctx)
	waitReady(t, ready)

	if err := os.WriteFile(filepath.Join(enabled, "web.yml"), []byte(service("web")), 0o644); err != nil {
		t.Fatal(err)
	}
	mon.Reload()

	mon.mu.Lock()
	cycle := mon.workers[0].cycle
	mon.mu.Unlock()
	if cycle < 11 {
		t.Fatalf("cycle after reload = %d, want preserved >= 11", cycle)
	}
}

func TestMonitorReloadRejectsInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"profiles", "enabled", "run"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	enabled := filepath.Join(dir, "enabled")
	global := filepath.Join(dir, "sermo.yml")
	valid := fmt.Sprintf(`engine:
  interval: 100ms
paths:
  profiles: [%q]
  enabled: [%q]
  runtime: %q
defaults:
  policy: { cooldown: 1m }
`, filepath.Join(dir, "profiles"), enabled, filepath.Join(dir, "run"))
	if err := os.WriteFile(global, []byte(valid), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(enabled, "web.yml"), []byte(`kind: service
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
	deps := Deps{Interval: 100 * time.Millisecond, Now: time.Now, Emit: func(Event) {}}
	collector := metrics.New(metrics.OSReader{})
	workers, _ := BuildWorkers(cfg, deps, collector)
	workers[0].cycle = 5

	ready := NewReadiness("systemd", 1, 0)
	mon := NewMonitor(cfg, deps, Scheduler{Interval: 20 * time.Millisecond}, ready, collector, nil)
	mon.ConfigPath = global
	mon.Logger = slog.Default()
	mon.Init(workers, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mon.Run(ctx)
	waitReady(t, ready)

	invalid := fmt.Sprintf(`engine:
  interval: notaduration
paths:
  profiles: [%q]
  enabled: [%q]
  runtime: %q
defaults:
  policy: { cooldown: 1m }
`, filepath.Join(dir, "profiles"), enabled, filepath.Join(dir, "run"))
	if err := os.WriteFile(global, []byte(invalid), 0o644); err != nil {
		t.Fatal(err)
	}
	mon.mu.Lock()
	before := mon.workers[0].cycle
	mon.mu.Unlock()

	mon.Reload()

	mon.mu.Lock()
	after := mon.workers[0].cycle
	mon.mu.Unlock()
	if after < before {
		t.Fatalf("cycle after rejected reload = %d, want preserved >= %d", after, before)
	}
	if rep := ready.Report(context.Background()); !rep.Ready || rep.Status != "ok" {
		t.Fatalf("readiness = %+v, want ready during rejected reload", rep)
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
