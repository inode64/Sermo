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

	// The shipped config points paths.profiles at the installed /usr/share/sermo
	// location, so override it to the source tree's profiles for this test —
	// exactly what `sermod run --profiles ./profiles` does.
	profiles := filepath.Join(filepath.Dir(filepath.Dir(global)), "profiles")
	cfg, err := config.Load(global, config.WithProfilesDirs(profiles))
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

func TestParseArgsVerbose(t *testing.T) {
	for _, flag := range []string{"--verbose", "-v"} {
		parsed, err := parseArgs([]string{"run", flag, "--config", "/etc/sermo/sermo.yml"})
		if err != nil {
			t.Fatalf("parseArgs(%q): %v", flag, err)
		}
		if parsed.command != "run" || parsed.globalPath != "/etc/sermo/sermo.yml" || !parsed.verbose {
			t.Fatalf("parseArgs(%q) = %+v, want (run, /etc/sermo/sermo.yml, verbose)", flag, parsed)
		}
	}

	// Verbose defaults off.
	if parsed, err := parseArgs([]string{"run"}); err != nil || parsed.verbose {
		t.Fatalf("parseArgs(run) verbose = %v, err = %v; want false, nil", parsed.verbose, err)
	}
}

func TestParseArgsProfiles(t *testing.T) {
	// Both spellings, repeatable, accumulate in order.
	parsed, err := parseArgs([]string{"run", "--profiles", "/a", "--profiles=/b"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if got := parsed.profiles; len(got) != 2 || got[0] != "/a" || got[1] != "/b" {
		t.Fatalf("profiles = %v, want [/a /b]", got)
	}

	// Defaults to none.
	if parsed, err := parseArgs([]string{"run"}); err != nil || len(parsed.profiles) != 0 {
		t.Fatalf("parseArgs(run) profiles = %v, err = %v; want empty, nil", parsed.profiles, err)
	}

	// Missing value is an error.
	if _, err := parseArgs([]string{"run", "--profiles"}); err == nil {
		t.Fatal("parseArgs(--profiles) without value: want error, got nil")
	}
}

func TestWebListenAddr(t *testing.T) {
	cases := []struct {
		name       string
		web        any
		wantAddr   string
		wantReason bool // expect a non-empty disabled reason
	}{
		{"no web section", nil, "", true},
		{"port missing", map[string]any{}, "", true},
		{"port zero", map[string]any{"port": 0}, "", true},
		{"port not a number", map[string]any{"port": "8080"}, "", true},
		{"default address", map[string]any{"port": 8080}, "127.0.0.1:8080", false},
		{"explicit address", map[string]any{"port": 9000, "address": "0.0.0.0"}, "0.0.0.0:9000", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := map[string]any{}
			if tc.web != nil {
				raw["web"] = tc.web
			}
			cfg := &config.Config{Global: config.Global{Raw: raw}}
			addr, reason := webListenAddr(cfg)
			if addr != tc.wantAddr {
				t.Fatalf("addr = %q, want %q", addr, tc.wantAddr)
			}
			if (reason != "") != tc.wantReason {
				t.Fatalf("reason = %q, wantReason = %v", reason, tc.wantReason)
			}
		})
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
