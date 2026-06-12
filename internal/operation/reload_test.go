package operation

import (
	"context"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/execx"
)

// scriptedRunner is a fake execx.Runner: it records argv and returns a canned
// result keyed by the command name (the first argv element).
type scriptedRunner struct {
	calls   [][]string
	results map[string]execx.Result
}

func (r *scriptedRunner) Run(_ context.Context, name string, args ...string) (execx.Result, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	if res, ok := r.results[name]; ok {
		return res, nil
	}
	return execx.Result{}, nil
}

func (r *scriptedRunner) ran(name string) bool {
	for _, c := range r.calls {
		if c[0] == name {
			return true
		}
	}
	return false
}

func depsWith(runner execx.Runner) checks.Deps { return checks.Deps{Runner: runner} }

func TestReloadClosureNoSpecUsesBackendReload(t *testing.T) {
	mgr := &fakeManager{canReload: true}
	reload := reloadClosure(map[string]any{}, depsWith(&scriptedRunner{}), mgr, "systemd", "mysqld")
	if err := reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !mgr.did("reload mysqld") {
		t.Errorf("with no reload spec the engine must call Manager.Reload; calls=%v", mgr.calls)
	}
}

func TestReloadClosureLegacyCommandAlwaysRuns(t *testing.T) {
	mgr := &fakeManager{canReload: true} // even though the unit can reload...
	runner := &scriptedRunner{}
	tree := map[string]any{"commands": map[string]any{"reload": map[string]any{"command": []any{"nginx", "-s", "reload"}}}}
	reload := reloadClosure(tree, depsWith(runner), mgr, "systemd", "nginx")
	if err := reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !runner.ran("nginx") {
		t.Errorf("legacy commands.reload must run the command; calls=%v", runner.calls)
	}
	if mgr.did("reload nginx") {
		t.Errorf("legacy commands.reload must override the backend reload; calls=%v", mgr.calls)
	}
}

func TestReloadClosureAutoCommandPrefersBackendWhenSupported(t *testing.T) {
	mgr := &fakeManager{canReload: true}
	runner := &scriptedRunner{}
	tree := map[string]any{"reload": map[string]any{"command": []any{"nginx", "-s", "reload"}, "when": "auto"}}
	reload := reloadClosure(tree, depsWith(runner), mgr, "systemd", "nginx")
	if err := reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if runner.ran("nginx") {
		t.Errorf("auto reload must use the backend when it supports reload; calls=%v", runner.calls)
	}
	if !mgr.did("reload nginx") {
		t.Errorf("auto reload must call Manager.Reload when supported; calls=%v", mgr.calls)
	}
}

func TestReloadClosureAutoCommandFallsBackWhenUnsupported(t *testing.T) {
	mgr := &fakeManager{canReload: false} // the unit has no ExecReload / reload()
	runner := &scriptedRunner{}
	tree := map[string]any{"reload": map[string]any{"command": []any{"nginx", "-s", "reload"}}}
	reload := reloadClosure(tree, depsWith(runner), mgr, "systemd", "nginx")
	if err := reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if mgr.did("reload nginx") {
		t.Errorf("auto reload must NOT call the backend when it cannot reload; calls=%v", mgr.calls)
	}
	if !runner.ran("nginx") {
		t.Errorf("auto reload must run the native command when the init cannot reload; calls=%v", runner.calls)
	}
}

func TestReloadClosureSignalSentToMainPID(t *testing.T) {
	// MainPID resolves to this test process; the native reload sends USR1 to it.
	pid := os.Getpid()
	mgr := &fakeManager{canReload: false}
	runner := &scriptedRunner{results: map[string]execx.Result{
		"systemctl": {Stdout: strconv.Itoa(pid) + "\n"},
	}}
	got := make(chan os.Signal, 1)
	signal.Notify(got, syscall.SIGUSR1)
	defer signal.Stop(got)

	tree := map[string]any{"reload": map[string]any{"signal": "USR1", "when": "always"}}
	reload := reloadClosure(tree, depsWith(runner), mgr, "systemd", "myd")
	if err := reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("native signal reload did not deliver SIGUSR1 to the main pid")
	}
}

func TestReloadClosureSignalUsesPidfileWhenNoMainPID(t *testing.T) {
	// OpenRC has no MainPID; the signal target comes from the pidfile selector.
	pid := os.Getpid()
	dir := t.TempDir()
	pidfile := dir + "/svc.pid"
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := &fakeManager{canReload: false}
	got := make(chan os.Signal, 1)
	signal.Notify(got, syscall.SIGUSR2)
	defer signal.Stop(got)

	tree := map[string]any{
		"reload":    map[string]any{"signal": "USR2"},
		"processes": map[string]any{"main": map[string]any{"type": "pidfile", "path": pidfile}},
	}
	reload := reloadClosure(tree, depsWith(&scriptedRunner{}), mgr, "openrc", "svc")
	if err := reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("native signal reload did not deliver SIGUSR2 via the pidfile pid")
	}
}
