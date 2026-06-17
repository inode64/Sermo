package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"sermo/internal/app"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/metrics"
	"sermo/internal/servicemgr"
)

func TestRunRejectsInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"enabled", "catalog"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	global := filepath.Join(dir, "sermo.yml")
	content := fmt.Sprintf(`engine:
  interval: notaduration
paths:
  catalog: [%s]
  includes: [%s]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
`, filepath.Join(dir, "catalog"), filepath.Join(dir, "enabled"))
	if err := os.WriteFile(global, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := run([]string{"run", "--config", global}); code != exitConfigInvalid {
		t.Fatalf("run() exit = %d, want %d", code, exitConfigInvalid)
	}
}

// TestRepoDefaultConfigHasMonitorTargets guards the acceptance path
// `sermod run --config ./examples/sermo.yml`. The shipped config deliberately
// enables no services (operators add their own under an include dir such as
// `apps`), so the host watches are what must load, validate and build — they
// are the daemon's out-of-the-box monitor targets.
func TestRepoDefaultConfigHasMonitorTargets(t *testing.T) {
	global := repoConfigPath(t)

	// The shipped config points paths.catalog at the installed /usr/share/sermo
	// location, so override it to the source tree's catalog for this test —
	// exactly what `sermod run --catalog ./catalog` does.
	catalog := filepath.Join(filepath.Dir(filepath.Dir(global)), "catalog")
	cfg, err := config.Load(global, config.WithCatalogDirs(catalog))
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
	collector := metrics.New(metrics.OSReader{})
	deps := app.Deps{
		DefaultTimeout: 10 * time.Second,
		Interval:       30 * time.Second,
		ExecxRunner:    execx.CommandRunner{},
	}
	workers, workerWarns := app.BuildWorkers(cfg, deps, collector)
	watches, watchWarns := app.BuildWatches(cfg, deps, deps.Interval)
	if len(watchWarns) != 0 || len(workerWarns) != 0 {
		t.Fatalf("shipped config must build without warnings: worker_warns=%v watch_warns=%v", workerWarns, watchWarns)
	}
	if len(watches) == 0 {
		t.Fatalf("no runnable watches from the shipped config: services=%d watches_in_cfg=%d workers=%d",
			len(cfg.Services), len(watchesRaw), len(workers))
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

func TestParseArgsCatalog(t *testing.T) {
	// Both spellings, repeatable, accumulate in order.
	parsed, err := parseArgs([]string{"run", "--catalog", "/a", "--catalog=/b"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if got := parsed.catalog; len(got) != 2 || got[0] != "/a" || got[1] != "/b" {
		t.Fatalf("catalog = %v, want [/a /b]", got)
	}

	// Defaults to none.
	if parsed, err := parseArgs([]string{"run"}); err != nil || len(parsed.catalog) != 0 {
		t.Fatalf("parseArgs(run) catalog = %v, err = %v; want empty, nil", parsed.catalog, err)
	}

	// Missing value is an error.
	if _, err := parseArgs([]string{"run", "--catalog"}); err == nil {
		t.Fatal("parseArgs(--catalog) without value: want error, got nil")
	}
}

func TestParseArgsConfig(t *testing.T) {
	// Both --config and --config= forms.
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"run", "--config", "/etc/sermo/sermo.yml"}, "/etc/sermo/sermo.yml"},
		{[]string{"run", "--config=/tmp/c.yml"}, "/tmp/c.yml"},
		{[]string{"run", "--config=./local.yml", "-v"}, "./local.yml"},
	} {
		parsed, err := parseArgs(c.args)
		if err != nil {
			t.Fatalf("parseArgs(%v): %v", c.args, err)
		}
		if parsed.globalPath != c.want {
			t.Errorf("globalPath = %q, want %q for %v", parsed.globalPath, c.want, c.args)
		}
	}

	// Missing value errors (same helper path as catalog).
	if _, err := parseArgs([]string{"run", "--config"}); err == nil {
		t.Fatal("parseArgs(--config) without value: want error, got nil")
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
		{"port not a number", map[string]any{"port": "abc"}, "", true},
		{"quoted port accepted", map[string]any{"port": "8080"}, "127.0.0.1:8080", false},
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
			p := filepath.Join(dir, "examples", "sermo.yml")
			if _, err := os.Stat(p); err == nil {
				return p
			}
			t.Fatalf("examples/sermo.yml not found under module root %s", dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func TestWebAuthFromConfig(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"web": map[string]any{
			"password":       "admin-pw",
			"guest_password": "guest-pw",
			"guest":          true,
		},
	}}}
	auth := webAuth(cfg)
	if auth.AdminPassword != "admin-pw" || auth.GuestPassword != "guest-pw" || !auth.AnonymousGuest {
		t.Fatalf("auth = %+v", auth)
	}

	empty := webAuth(&config.Config{Global: config.Global{Raw: map[string]any{}}})
	if empty.AdminPassword != "" || empty.GuestPassword != "" || empty.AnonymousGuest {
		t.Fatalf("auth without web section = %+v, want zero value", empty)
	}
}

func TestEngineAndNotifierAccessors(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"engine":    map[string]any{"backend": "openrc", "interval": "30s"},
		"notifiers": map[string]any{"ops": map[string]any{"type": "slack"}},
	}}}
	if got := app.EngineString(cfg, "backend"); got != "openrc" {
		t.Fatalf("engineString(backend) = %q, want openrc", got)
	}
	if got := app.EngineString(cfg, "missing"); got != "" {
		t.Fatalf("engineString(missing) = %q, want empty", got)
	}
	if raw := cfg.Notifiers(); len(raw) != 1 {
		t.Fatalf("Notifiers() = %v, want the ops entry", raw)
	}

	bare := &config.Config{Global: config.Global{Raw: map[string]any{}}}
	if app.EngineString(bare, "backend") != "" || bare.Notifiers() != nil {
		t.Fatal("accessors on an empty config must return zero values")
	}

	// Exercise improved coercion (now via cfgval): string forms for ints and durations are accepted
	// (previously engineInt only accepted bare numeric types; durations already string-only).
	cfg2 := &config.Config{Global: config.Global{Raw: map[string]any{
		"engine": map[string]any{
			"max_parallel_checks":     "16", // string form (e.g. from some expansions)
			"default_timeout":         "45s",
			"max_parallel_operations": 4, // int form
		},
	}}}
	if got := app.EngineInt(cfg2, "max_parallel_checks", 8); got != 16 {
		t.Fatalf("EngineInt string-num = %d, want 16", got)
	}
	if got := app.EngineInt(cfg2, "max_parallel_operations", 2); got != 4 {
		t.Fatalf("EngineInt bare-int = %d, want 4", got)
	}
	if got := app.EngineDuration(cfg2, "default_timeout", 10*time.Second); got != 45*time.Second {
		t.Fatalf("EngineDuration = %v, want 45s", got)
	}
	if got := app.EngineDuration(cfg2, "missing_dur", 99*time.Second); got != 99*time.Second {
		t.Fatalf("EngineDuration missing fallback failed")
	}
	var nilCfg *config.Config
	if nilCfg.Notifiers() != nil {
		t.Fatal("Notifiers() on a nil config must return nil")
	}
}

// TestRunSmokeLifecycle boots the whole daemon against a temp config — workers,
// watches, state store, web server — waits until it reports ready, exercises a
// SIGHUP reload, and shuts it down with SIGTERM expecting a clean exit. This is
// the integration smoke for run(): the startup path no unit test covers.
func TestRunSmokeLifecycle(t *testing.T) {
	if _, err := servicemgr.NewDetector().Detect(context.Background(), servicemgr.BackendAuto); err != nil {
		t.Skipf("no init backend available: %v", err)
	}

	dir := t.TempDir()
	for _, sub := range []string{"enabled", "catalog"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	port := freeTCPPort(t)
	global := filepath.Join(dir, "sermo.yml")
	content := fmt.Sprintf(`engine:
  backend: auto
  interval: 1s
paths:
  catalog: [%s]
  includes: [%s]
  runtime: %s
  state: %s
defaults:
  policy: { cooldown: 5m }
web:
  address: 127.0.0.1
  port: %d
watches:
  smoke:
    monitor: disabled
    check: { type: oom }
    then: { hook: { command: [/bin/true] } }
`, filepath.Join(dir, "catalog"), filepath.Join(dir, "enabled"),
		filepath.Join(dir, "runtime"), filepath.Join(dir, "state"), port)
	if err := os.WriteFile(global, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	done := make(chan int, 1)
	go func() { done <- run([]string{"run", "--config", global}) }()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitHTTPOK(t, base+"/readyz", 15*time.Second)
	waitHTTPOK(t, base+"/livez", 2*time.Second)

	// Reload in place (same config) and require the daemon to stay ready.
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	waitHTTPOK(t, base+"/readyz", 5*time.Second)

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("run() exit = %d, want 0", code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("daemon did not stop after SIGTERM")
	}
}

// freeTCPPort grabs an ephemeral loopback port for the smoke web server.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// waitHTTPOK polls url until it answers 200 or the deadline passes.
func waitHTTPOK(t *testing.T, url string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		resp, err := http.Get(url) //nolint:gosec // loopback test URL
		if err == nil {
			ok := resp.StatusCode == http.StatusOK
			resp.Body.Close()
			if ok {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s not OK within %s (last err %v)", url, within, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
