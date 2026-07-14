package operation

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/execx"
	"sermo/internal/process"
)

// scriptedRunner is a fake execx.Runner: it records argv and returns a canned
// result keyed by the command name (the first argv element).
type scriptedRunner struct {
	calls          [][]string
	results        map[string]execx.Result
	errs           map[string]error
	respectContext bool
}

func (r *scriptedRunner) Run(ctx context.Context, name string, args ...string) (execx.Result, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	if r.respectContext {
		if err := ctx.Err(); err != nil {
			return execx.Result{}, err
		}
	}
	if res, ok := r.results[name]; ok {
		return res, r.errs[name]
	}
	return execx.Result{}, r.errs[name]
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

func reloadClosureForTest(tree map[string]any, deps checks.Deps, mgr Manager, unit string) func(context.Context) error {
	return reloadClosure(tree, deps, mgr, "systemd", unit, process.Discoverer{}, nil)
}

type reloadProcessReader struct {
	ids map[int]process.Identity
}

func (r reloadProcessReader) PIDs() ([]int, error) {
	pids := make([]int, 0, len(r.ids))
	for pid := range r.ids {
		pids = append(pids, pid)
	}
	return pids, nil
}

func (r reloadProcessReader) Identity(pid int) (process.Identity, bool) {
	id, ok := r.ids[pid]
	return id, ok
}

func reloadDiscoverer(ids map[int]process.Identity) process.Discoverer {
	return process.Discoverer{
		Reader: reloadProcessReader{ids: ids},
		ResolveUser: func(name string) (uint32, bool) {
			if name == "svcuser" {
				return 1001, true
			}
			return 0, false
		},
	}
}

func TestReloadClosureNoSpecUsesBackendReload(t *testing.T) {
	mgr := &fakeManager{canReload: true}
	reload := reloadClosureForTest(map[string]any{}, depsWith(&scriptedRunner{}), mgr, "mysqld")
	if err := reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !mgr.did("reload mysqld") {
		t.Errorf("with no reload spec the engine must call Manager.Reload; calls=%v", mgr.calls)
	}
}

func TestReloadClosureNoSpecRejectsUnsupportedBackendReload(t *testing.T) {
	mgr := &fakeManager{canReload: false}
	reload := reloadClosureForTest(map[string]any{}, depsWith(&scriptedRunner{}), mgr, "mysqld")

	err := reload(context.Background())
	if err == nil || !strings.Contains(err.Error(), "does not support reload") {
		t.Fatalf("reload err = %v, want unsupported reload", err)
	}
	if mgr.did("reload mysqld") {
		t.Fatalf("unsupported reload called backend reload; calls=%v", mgr.calls)
	}
	if !mgr.did("supports-reload mysqld") {
		t.Fatalf("unsupported reload did not check backend support; calls=%v", mgr.calls)
	}
}

func TestReloadClosureAutoCommandPrefersBackendWhenSupported(t *testing.T) {
	mgr := &fakeManager{canReload: true}
	runner := &scriptedRunner{}
	tree := map[string]any{"reload": map[string]any{"command": []any{"nginx", "-s", "reload"}, "when": "auto"}}
	reload := reloadClosureForTest(tree, depsWith(runner), mgr, "nginx")
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
	reload := reloadClosureForTest(tree, depsWith(runner), mgr, "nginx")
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

func TestReloadClosureAutoCommandReportsBackendSupportError(t *testing.T) {
	mgr := &fakeManager{reloadSupportErr: errors.New("systemctl unavailable")}
	runner := &scriptedRunner{}
	tree := map[string]any{"reload": map[string]any{"command": []any{"nginx", "-s", "reload"}}}
	reload := reloadClosureForTest(tree, depsWith(runner), mgr, "nginx")

	err := reload(context.Background())
	if err == nil || !strings.Contains(err.Error(), "reload support: systemctl unavailable") {
		t.Fatalf("reload err = %v, want backend support error", err)
	}
	if runner.ran("nginx") {
		t.Fatalf("reload fell back after support error; calls=%v", runner.calls)
	}
}

func TestReloadClosureCommandWithoutRunnerReturnsError(t *testing.T) {
	mgr := &fakeManager{canReload: false}
	tree := map[string]any{"reload": map[string]any{"command": []any{"sermo-no-such-command-xyz"}, "when": "always"}}
	reload := reloadClosureForTest(tree, checks.Deps{}, mgr, "svc")

	if err := reload(context.Background()); err == nil {
		t.Fatal("reload without an injected runner returned nil, want command error")
	}
}

func TestParseReloadSpecIgnoresEmptyCommand(t *testing.T) {
	tree := map[string]any{"reload": map[string]any{"command": []any{}}}
	if spec := parseReloadSpec(tree); spec != nil {
		t.Fatalf("parseReloadSpec(empty command) = %+v, want nil", spec)
	}
}

func TestReloadClosureCommandDidNotStartWithoutRunnerError(t *testing.T) {
	mgr := &fakeManager{canReload: false}
	runner := &scriptedRunner{results: map[string]execx.Result{
		"reloadctl": {ExitCode: -1},
	}}
	tree := map[string]any{"reload": map[string]any{"command": []any{"reloadctl"}, "when": "always"}}
	reload := reloadClosureForTest(tree, depsWith(runner), mgr, "svc")

	err := reload(context.Background())
	if err == nil || err.Error() != execx.CommandDidNotStart {
		t.Fatalf("reload err = %v, want %q", err, execx.CommandDidNotStart)
	}
}

func TestReloadClosureCommandStartErrorUsesOperatorMessage(t *testing.T) {
	mgr := &fakeManager{canReload: false}
	runner := &scriptedRunner{
		results: map[string]execx.Result{"reloadctl": {ExitCode: -1}},
		errs:    map[string]error{"reloadctl": errors.New("run reloadctl: executable file not found")},
	}
	tree := map[string]any{"reload": map[string]any{"command": []any{"reloadctl"}, "when": "always"}}
	reload := reloadClosureForTest(tree, depsWith(runner), mgr, "svc")

	err := reload(context.Background())
	if err == nil || err.Error() != "executable file not found" {
		t.Fatalf("reload err = %v, want stripped operator message", err)
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
	reload := reloadClosureForTest(tree, depsWith(runner), mgr, "myd")
	if err := reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("native signal reload did not deliver SIGUSR1 to the main pid")
	}
}

func TestReloadClosureSignalHonorsCanceledContext(t *testing.T) {
	mgr := &fakeManager{canReload: false}
	runner := &scriptedRunner{
		results:        map[string]execx.Result{"systemctl": {Stdout: strconv.Itoa(os.Getpid()) + "\n"}},
		respectContext: true,
	}
	tree := map[string]any{"reload": map[string]any{"signal": "USR1", "when": "always"}}
	reload := reloadClosureForTest(tree, depsWith(runner), mgr, "myd")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := reload(ctx)
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("reload err = %v, want cancelled", err)
	}
	if runner.ran("systemctl") {
		t.Fatalf("reload tried MainPID resolution after cancellation; calls=%v", runner.calls)
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
		"pidfile": pidfile,
		"reload":  map[string]any{"signal": "USR2"},
	}
	selectors := []process.Selector{
		{Name: "main", Type: process.SelectorPidfile, Paths: []string{pidfile}},
		{Name: "identity", Type: process.SelectorCommandMatch, Exe: "/usr/sbin/svc", User: "svcuser"},
	}
	discoverer := reloadDiscoverer(map[int]process.Identity{
		pid: {PID: pid, UID: 1001, Exe: "/usr/sbin/svc", ExeOK: true},
	})
	reload := reloadClosure(tree, depsWith(&scriptedRunner{}), mgr, "openrc", "svc", discoverer, selectors)
	if err := reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("native signal reload did not deliver SIGUSR2 via the pidfile pid")
	}
}

func TestReloadClosureSignalPidfileRequiresStrictIdentity(t *testing.T) {
	dir := t.TempDir()
	pidfile := dir + "/svc.pid"
	if err := os.WriteFile(pidfile, []byte("999999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := &fakeManager{canReload: false}
	tree := map[string]any{
		"pidfile": pidfile,
		"reload":  map[string]any{"signal": "HUP"},
	}
	selectors := []process.Selector{{Name: "main", Type: process.SelectorPidfile, Paths: []string{pidfile}}}
	reload := reloadClosure(tree, depsWith(&scriptedRunner{}), mgr, "openrc", "svc", reloadDiscoverer(nil), selectors)

	err := reload(context.Background())
	if err == nil || !strings.Contains(err.Error(), "does not match any process selector with exact exe and user") {
		t.Fatalf("reload err = %v, want strict identity failure", err)
	}
}

func TestReloadClosureSignalMainPIDRequiresStrictIdentity(t *testing.T) {
	wrongPID := 424242
	mgr := &fakeManager{canReload: false}
	runner := &scriptedRunner{results: map[string]execx.Result{
		"systemctl": {Stdout: strconv.Itoa(wrongPID) + "\n"},
	}}
	tree := map[string]any{"reload": map[string]any{"signal": "HUP", "when": "always"}}
	selectors := []process.Selector{{
		Name: "main", Type: process.SelectorCommandMatch, Exe: "/usr/sbin/svc", User: "svcuser",
	}}
	reload := reloadClosure(tree, depsWith(runner), mgr, "systemd", "svc", reloadDiscoverer(nil), selectors)

	err := reload(context.Background())
	if err == nil || !strings.Contains(err.Error(), "MainPID 424242 does not match any process selector") {
		t.Fatalf("reload err = %v, want strict MainPID identity failure", err)
	}
}
