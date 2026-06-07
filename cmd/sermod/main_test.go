package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sermo/internal/app"
	"sermo/internal/config"
	"sermo/internal/metrics"
)

func TestRunRejectsInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"enabled", "profiles"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	global := filepath.Join(dir, "sermo.yml")
	content := fmt.Sprintf(`engine:
  interval: notaduration
paths:
  profiles: [%s]
  enabled: [%s]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
`, filepath.Join(dir, "profiles"), filepath.Join(dir, "enabled"))
	if err := os.WriteFile(global, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := run([]string{"run", "--config", global}); code != exitConfigInvalid {
		t.Fatalf("run() exit = %d, want %d", code, exitConfigInvalid)
	}
}

// TestRepoDefaultConfigHasMonitorTargets guards the acceptance path
// `sermod run --config ./configs/sermo.yml`: relative paths must load the
// bundled services and the global watches section must be present (watches are
// disabled by default, so services are what keep the daemon from exiting early).
func TestRepoDefaultConfigHasMonitorTargets(t *testing.T) {
	global := repoConfigPath(t)

	cfg, err := config.Load(global)
	if err != nil {
		t.Fatalf("Load(%q): %v", global, err)
	}
	if issues := config.Validate(cfg); len(issues) > 0 {
		t.Fatalf("Validate: %v", issues)
	}

	watchesRaw, ok := cfg.Global.Raw["watches"].(map[string]any)
	if !ok || len(watchesRaw) == 0 {
		t.Fatal("expected non-empty watches section in repo default config")
	}
	if len(cfg.Services) == 0 {
		t.Fatalf("expected enabled services from %q, got none (check paths.profiles/enabled are relative to the config file)", global)
	}

	collector := metrics.New(metrics.OSReader{})
	deps := app.Deps{
		DefaultTimeout: 10 * time.Second,
		Interval:       30 * time.Second,
	}
	workers, workerWarns := app.BuildWorkers(cfg, deps, collector)
	watches, watchWarns := app.BuildWatches(cfg, deps, deps.Interval)
	if len(workers) == 0 && len(watches) == 0 {
		t.Fatalf("no runnable workers or watches: services=%d watches_in_cfg=%d worker_warns=%v watch_warns=%v",
			len(cfg.Services), len(watchesRaw), workerWarns, watchWarns)
	}
}

func repoConfigPath(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			p := filepath.Join(dir, "configs", "sermo.yml")
			if _, err := os.Stat(p); err == nil {
				return p
			}
			t.Fatalf("configs/sermo.yml not found under module root %s", dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}