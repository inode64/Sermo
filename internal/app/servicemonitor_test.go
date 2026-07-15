package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/execx"
	"sermo/internal/notify"
	"sermo/internal/servicemgr"
)

type monitorUserRunner struct {
	user string
	name string
	args []string
}

func (r *monitorUserRunner) Run(context.Context, string, ...string) (execx.Result, error) {
	return execx.Result{ExitCode: -1}, nil
}

func (r *monitorUserRunner) RunUser(_ context.Context, user, name string, args ...string) (execx.Result, error) {
	r.user = user
	r.name = name
	r.args = append([]string(nil), args...)
	return execx.Result{ExitCode: 0, Stdout: "ok\n"}, nil
}

func monitorTestDeps() Deps {
	return Deps{
		Notifiers:   map[string]notify.Notifier{"ops": &fakeNotifier{name: "ops"}},
		ExecxRunner: execx.CommandRunner{},
		Now:         time.Now,
		Emit:        func(Event) {},
	}
}

func TestVersionMonitorAdvancesSettling(t *testing.T) {
	ready := NewReadiness(string(servicemgr.BackendSystemd), 1, 0)
	settling := NewSettling(ready)
	settling.Reset([]string{SettlingWatchKey("apache:version")})
	ready.ExpectFirstCycles(1)

	tree := map[string]any{
		"commands": map[string]any{"version": map[string]any{"command": []any{"/bin/true"}}},
		"version":  map[string]any{"on_change": map[string]any{"notify": []any{"ops"}}},
	}
	deps := monitorTestDeps()
	deps.Settling = settling

	w, warn := versionMonitor("apache", tree, deps, time.Minute)
	if warn != "" || w == nil {
		t.Fatalf("warn=%q w=%v", warn, w)
	}
	w.RunCycle(context.Background())
	if !settling.Observed(SettlingWatchKey("apache:version")) {
		t.Fatal("version monitor must complete startup observation")
	}
	if !ready.Report(context.Background()).Ready {
		t.Fatal("version monitor must advance daemon readiness")
	}
}

func TestVersionMonitor(t *testing.T) {
	tree := map[string]any{
		"commands": map[string]any{"version": map[string]any{"command": []any{"apachectl", "-v"}}},
		"version":  map[string]any{"on_change": map[string]any{"notify": []any{"ops"}}},
	}
	w, warn := versionMonitor("apache", tree, monitorTestDeps(), time.Minute)
	if warn != "" || w == nil {
		t.Fatalf("warn=%q w=%v", warn, w)
	}
	if w.Name != "apache:version" || w.CheckType != "command" {
		t.Errorf("watch = %+v", w)
	}
	if len(w.Notifiers) != 1 {
		t.Errorf("notifiers = %v (want ops)", w.Notifiers)
	}

	// version.on_change but no version command in the service -> warning.
	noCmd := map[string]any{"version": map[string]any{"on_change": map[string]any{}}}
	if _, warn := versionMonitor("x", noCmd, monitorTestDeps(), time.Minute); warn == "" {
		t.Error("a missing version command should warn")
	}

	// No version block -> no watch, no warning.
	if w, warn := versionMonitor("x", map[string]any{}, monitorTestDeps(), time.Minute); w != nil || warn != "" {
		t.Errorf("no version block should yield nil/no-warn, got %v/%q", w, warn)
	}
}

func TestVersionMonitorPreservesCommandUser(t *testing.T) {
	runner := &monitorUserRunner{}
	tree := map[string]any{
		"commands": map[string]any{"version": map[string]any{"command": []any{"postgres", "--version"}, "user": "postgres"}},
		"version":  map[string]any{"on_change": map[string]any{"notify": []any{"ops"}}},
	}
	deps := monitorTestDeps()
	deps.ExecxRunner = runner

	w, warn := versionMonitor("postgres", tree, deps, time.Minute)
	if warn != "" || w == nil {
		t.Fatalf("warn=%q w=%v", warn, w)
	}
	if res := w.Check.Run(context.Background()); !res.OK {
		t.Fatalf("version monitor check should pass: %s", res.Message)
	}
	if runner.user != "postgres" || runner.name != "postgres" || len(runner.args) != 1 || runner.args[0] != "--version" {
		t.Fatalf("RunUser call = user=%q name=%q args=%v", runner.user, runner.name, runner.args)
	}
}

func TestVersionMonitorLevel(t *testing.T) {
	build := func(level any, outs ...string) *Watch {
		t.Helper()
		on := map[string]any{"notify": []any{"ops"}}
		if level != nil {
			on["level"] = level
		}
		tree := map[string]any{
			"commands": map[string]any{"version": map[string]any{"command": []any{"app", "--version"}}},
			"version":  map[string]any{"on_change": on},
		}
		deps := monitorTestDeps()
		deps.ExecxRunner = &sequenceRunner{stdout: outs}
		w, warn := versionMonitor("app", tree, deps, time.Minute)
		if warn != "" || w == nil {
			t.Fatalf("warn=%q w=%v", warn, w)
		}
		return w
	}

	// minor level: priming cycle, then a patch bump (ignored), then a minor bump
	// (fires).
	w := build("minor", "app 1.4.2", "app 1.4.7", "app 1.5.0")
	if r := w.Check.Run(context.Background()); !r.OK {
		t.Fatalf("priming cycle should be OK, got %+v", r)
	}
	if r := w.Check.Run(context.Background()); !r.OK {
		t.Fatalf("patch bump must not fire at minor level, got %+v", r)
	}
	if r := w.Check.Run(context.Background()); r.OK {
		t.Fatalf("minor bump must fire at minor level")
	}

	// default (no level) is patch: any a.b.c change fires.
	wd := build(nil, "app 1.4.2", "app 1.4.3")
	wd.Check.Run(context.Background())
	if r := wd.Check.Run(context.Background()); r.OK {
		t.Fatalf("patch bump must fire at default (patch) level")
	}

	// invalid level -> warning, no watch.
	bad := map[string]any{
		"commands": map[string]any{"version": map[string]any{"command": []any{"app", "--version"}}},
		"version":  map[string]any{"on_change": map[string]any{"level": "epoch"}},
	}
	if _, warn := versionMonitor("app", bad, monitorTestDeps(), time.Minute); warn == "" {
		t.Error("an invalid version.on_change.level should warn")
	}
}

func TestConfigMonitor(t *testing.T) {
	tree := map[string]any{
		"preflight": map[string]any{"config": map[string]any{"type": "command", "command": []any{"apachectl", "configtest"}}},
		"config":    map[string]any{"on_change": map[string]any{"notify": []any{"ops"}}, "path": []any{"/etc/apache2/apache2.conf"}},
	}
	w, warn := configMonitor("apache", tree, monitorTestDeps(), time.Minute)
	if warn != "" || w == nil {
		t.Fatalf("warn=%q w=%v", warn, w)
	}
	if w.Name != "apache:config" || w.CheckType != "config" {
		t.Errorf("watch = %+v", w)
	}

	// config.on_change but neither preflight.config nor a path -> warning.
	bare := map[string]any{"config": map[string]any{"on_change": map[string]any{"notify": []any{"ops"}}}}
	if _, warn := configMonitor("x", bare, monitorTestDeps(), time.Minute); warn == "" {
		t.Error("config monitor with no command/path should warn")
	}

	// path-only (no config test command) is allowed.
	pathOnly := map[string]any{"config": map[string]any{"on_change": map[string]any{}, "path": []any{"/etc/x.conf"}}}
	if w, warn := configMonitor("x", pathOnly, monitorTestDeps(), time.Minute); warn != "" || w == nil {
		t.Errorf("path-only config monitor should build: %q", warn)
	}
}

func TestConfigMonitorPreservesCommandUser(t *testing.T) {
	runner := &monitorUserRunner{}
	tree := map[string]any{
		"preflight": map[string]any{"config": map[string]any{"type": "command", "command": []any{"postgres", "--check"}, "user": "postgres"}},
		"config":    map[string]any{"on_change": map[string]any{"notify": []any{"ops"}}},
	}
	deps := monitorTestDeps()
	deps.ExecxRunner = runner

	w, warn := configMonitor("postgres", tree, deps, time.Minute)
	if warn != "" || w == nil {
		t.Fatalf("warn=%q w=%v", warn, w)
	}
	if res := w.Check.Run(context.Background()); !res.OK {
		t.Fatalf("config monitor check should pass: %s", res.Message)
	}
	if runner.user != "postgres" || runner.name != "postgres" || len(runner.args) != 1 || runner.args[0] != "--check" {
		t.Fatalf("RunUser call = user=%q name=%q args=%v", runner.user, runner.name, runner.args)
	}
}

func TestServiceChangeMonitorsInheritDryRun(t *testing.T) {
	tree := map[string]any{
		"dry_run":  true,
		"commands": map[string]any{"version": map[string]any{"command": []any{"app", "--version"}}},
		"version":  map[string]any{"on_change": map[string]any{"notify": []any{"ops"}}},
		"preflight": map[string]any{
			"config": map[string]any{"type": "command", "command": []any{"app", "--check"}},
		},
		"config": map[string]any{"on_change": map[string]any{"notify": []any{"ops"}}},
	}

	versionWatch, warn := versionMonitor("app", tree, monitorTestDeps(), time.Minute)
	if warn != "" || versionWatch == nil {
		t.Fatalf("version monitor warn=%q watch=%v", warn, versionWatch)
	}
	if !versionWatch.DryRun {
		t.Fatal("version monitor should inherit service dry_run")
	}

	configWatch, warn := configMonitor("app", tree, monitorTestDeps(), time.Minute)
	if warn != "" || configWatch == nil {
		t.Fatalf("config monitor warn=%q watch=%v", warn, configWatch)
	}
	if !configWatch.DryRun {
		t.Fatal("config monitor should inherit service dry_run")
	}
}
