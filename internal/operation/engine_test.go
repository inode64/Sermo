package operation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/locks"
	"sermo/internal/metrics"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

type fakeManager struct {
	stopErr   error
	startErr  error
	reloadErr error
	resumeErr error
	resetErr  error
	status    servicemgr.Status
	statusErr error
	calls     []string
	errOn     map[string]error // per-call ("start <unit>"/"stop <unit>") error override
	canReload bool             // SupportsReload result
}

func (m *fakeManager) Start(_ context.Context, s string) error {
	m.calls = append(m.calls, "start "+s)
	if m.errOn != nil {
		if err, ok := m.errOn["start "+s]; ok {
			return err
		}
	}
	return m.startErr
}

func (m *fakeManager) Stop(_ context.Context, s string) error {
	m.calls = append(m.calls, "stop "+s)
	if m.errOn != nil {
		if err, ok := m.errOn["stop "+s]; ok {
			return err
		}
	}
	return m.stopErr
}

func (m *fakeManager) Reload(_ context.Context, s string) error {
	m.calls = append(m.calls, "reload "+s)
	return m.reloadErr
}

func (m *fakeManager) Resume(_ context.Context, s string) error {
	m.calls = append(m.calls, "resume "+s)
	return m.resumeErr
}

func (m *fakeManager) SupportsReload(_ context.Context, s string) (bool, error) {
	m.calls = append(m.calls, "supports-reload "+s)
	return m.canReload, nil
}

func (m *fakeManager) Status(_ context.Context, s string) (servicemgr.ServiceStatus, error) {
	return servicemgr.ServiceStatus{Status: m.status}, m.statusErr
}

func (m *fakeManager) ResetState(_ context.Context, s string) error {
	m.calls = append(m.calls, "reset "+s)
	return m.resetErr
}

func (m *fakeManager) did(call string) bool {
	for _, c := range m.calls {
		if c == call {
			return true
		}
	}
	return false
}

type harness struct {
	mgr           *fakeManager
	lockErr       error
	released      int
	named         []locks.Lock
	guardBlocked  bool
	guardReason   string
	guardErr      error
	preflight     checks.Outcome
	postflight    checks.Outcome
	discoverSteps [][]process.Process
	discoverErrs  []error
	discoverCalls int
	reaper        process.Reaper
	killPolicy    process.KillPolicy
	emitted       []Result
}

func defaultHarness() *harness {
	return &harness{
		mgr:        &fakeManager{status: servicemgr.StatusActive},
		preflight:  checks.Outcome{OK: true},
		postflight: checks.Outcome{OK: true},
	}
}

func (h *harness) discover() ([]process.Process, error) {
	i := h.discoverCalls
	h.discoverCalls++
	var err error
	if i < len(h.discoverErrs) {
		err = h.discoverErrs[i]
	}
	if i < len(h.discoverSteps) {
		return h.discoverSteps[i], err
	}
	if len(h.discoverSteps) == 0 {
		return nil, err
	}
	return h.discoverSteps[len(h.discoverSteps)-1], err
}

func (h *harness) engine() Engine {
	return Engine{
		Service: "mysql-main",
		Unit:    "mysqld",
		Backend: "systemd",
		Manager: h.mgr,
		AcquireLock: func(time.Duration) (func() error, error) {
			if h.lockErr != nil {
				return nil, h.lockErr
			}
			return func() error { h.released++; return nil }, nil
		},
		LockTTL:    time.Minute,
		NamedLocks: func() ([]locks.Lock, error) { return h.named, nil },
		Guard:      func(context.Context, string) (bool, string, error) { return h.guardBlocked, h.guardReason, h.guardErr },
		Preflight:  func(context.Context) checks.Outcome { return h.preflight },
		Postflight: func(context.Context) checks.Outcome { return h.postflight },
		ResumeFunc: func(ctx context.Context) error { return h.mgr.Resume(ctx, "mysqld") },
		Discover:   h.discover,
		Reaper:     h.reaper,
		KillPolicy: h.killPolicy,
		Sleep:      func(time.Duration) {},
		Emit:       func(r Result) { h.emitted = append(h.emitted, r) },
	}
}

func (h *harness) action(t *testing.T, action string) Result {
	t.Helper()
	res := h.engine().Do(context.Background(), action)
	if len(h.emitted) != 1 {
		t.Fatalf("Emit called %d times, want exactly 1", len(h.emitted))
	}
	if h.emitted[0].Status != res.Status {
		t.Fatalf("emitted result %q != returned %q", h.emitted[0].Status, res.Status)
	}
	return res
}

func (h *harness) restart(t *testing.T) Result {
	t.Helper()
	return h.action(t, "restart")
}

func TestRestartOK(t *testing.T) {
	h := defaultHarness()
	res := h.restart(t)
	if !res.OK() {
		t.Fatalf("status = %q, want ok (%s)", res.Status, res.Message)
	}
	if !h.mgr.did("stop mysqld") || !h.mgr.did("start mysqld") {
		t.Fatalf("expected stop then start, calls=%v", h.mgr.calls)
	}
	if h.released != 1 {
		t.Errorf("op lock released %d times, want 1", h.released)
	}
}

func TestSectionRunnerBuildWarningBlocksRequiredPreflight(t *testing.T) {
	tree := map[string]any{
		"preflight": map[string]any{
			"cpu": map[string]any{"type": "metric", "name": "cpu", "op": ">", "value": "90"},
		},
	}

	out := sectionRunner(tree, "preflight", checks.Deps{Service: "web", DefaultTimeout: time.Second}, nil)(context.Background())
	if out.OK {
		t.Fatalf("outcome OK = true, want required build warning to fail: %+v", out)
	}
	if len(out.Results) != 1 || out.Results[0].Check != "cpu" || out.Results[0].OK || out.Results[0].Optional {
		t.Fatalf("results = %+v, want required failed build-warning result", out.Results)
	}
}

func TestSectionRunnerMetricSampleEnablesMetricPreflight(t *testing.T) {
	tree := map[string]any{
		"preflight": map[string]any{
			"load": map[string]any{"type": "metric", "name": "load1", "scope": "system", "op": "<", "value": "10"},
		},
	}
	sample := func(context.Context) checks.MetricReader {
		return func(scope, name string) (metrics.Reading, bool) {
			if scope != "system" || name != "load1" {
				return metrics.Reading{}, false
			}
			return metrics.Reading{Absolute: 1.5, HasAbsolute: true, Ready: true}, true
		}
	}

	out := sectionRunner(tree, "preflight", checks.Deps{Service: "web", DefaultTimeout: time.Second}, sample)(context.Background())
	if !out.OK {
		t.Fatalf("outcome OK = false, want metric preflight to pass with MetricSample: %+v", out)
	}
}

func TestSectionRunnerOptionalBuildWarningDoesNotBlock(t *testing.T) {
	tree := map[string]any{
		"preflight": map[string]any{
			"cpu": map[string]any{"type": "metric", "name": "cpu", "op": ">", "value": "90", "optional": true},
		},
	}

	out := sectionRunner(tree, "preflight", checks.Deps{Service: "web", DefaultTimeout: time.Second}, nil)(context.Background())
	if !out.OK {
		t.Fatalf("outcome OK = false, want optional build warning to pass: %+v", out)
	}
	if len(out.Results) != 1 || out.Results[0].Check != "cpu" || !out.Results[0].Optional {
		t.Fatalf("results = %+v, want optional build-warning result", out.Results)
	}
}

func TestResumeOK(t *testing.T) {
	h := defaultHarness()
	res := h.engine().Resume(context.Background())
	if !res.OK() {
		t.Fatalf("status = %q, want ok (%s)", res.Status, res.Message)
	}
	if !h.mgr.did("resume mysqld") {
		t.Fatalf("expected resume call, calls=%v", h.mgr.calls)
	}
}

func TestResumeUnsupported(t *testing.T) {
	h := defaultHarness()
	engine := h.engine()
	engine.ResumeFunc = nil
	res := engine.Resume(context.Background())
	if res.Status != ResultFailed {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if !strings.Contains(res.Message, "unsupported") {
		t.Fatalf("message = %q, want unsupported", res.Message)
	}
	if h.mgr.did("resume mysqld") {
		t.Fatalf("must not call manager without ResumeFunc, calls=%v", h.mgr.calls)
	}
}

func TestResumeServiceFailedAfterResume(t *testing.T) {
	h := defaultHarness()
	h.mgr.status = servicemgr.StatusFailed
	res := h.engine().Resume(context.Background())
	if res.Status != ResultFailed || res.Message != "service failed after resume" {
		t.Fatalf("res = %+v, want failed after resume", res)
	}
}

func TestStopReconcilesInitState(t *testing.T) {
	h := defaultHarness()
	res := h.engine().Stop(context.Background())
	if !res.OK() {
		t.Fatalf("stop status = %q (%s)", res.Status, res.Message)
	}
	// A clean stop (no residuals) must reconcile the init's recorded state.
	want := []string{"stop mysqld", "reset mysqld"}
	if !reflect.DeepEqual(h.mgr.calls, want) {
		t.Fatalf("calls = %v, want %v", h.mgr.calls, want)
	}
}

func TestRestartReconcilesBetweenStopAndStart(t *testing.T) {
	h := defaultHarness()
	res := h.restart(t)
	if !res.OK() {
		t.Fatalf("status = %q (%s)", res.Status, res.Message)
	}
	want := []string{"stop mysqld", "reset mysqld", "start mysqld"}
	if !reflect.DeepEqual(h.mgr.calls, want) {
		t.Fatalf("calls = %v, want stop->reset->start", h.mgr.calls)
	}
}

func TestStopWithResidualsSkipsReset(t *testing.T) {
	h := defaultHarness()
	// ForceKill is false, so a discovered residual is reported as an orphan and
	// the stop does not complete cleanly.
	h.discoverSteps = [][]process.Process{{{PID: 4242}}}
	res := h.engine().Stop(context.Background())
	if res.Status != ResultOrphanProcesses {
		t.Fatalf("status = %q, want orphan_processes", res.Status)
	}
	if h.mgr.did("reset mysqld") {
		t.Fatalf("must not reconcile init state while residuals remain, calls=%v", h.mgr.calls)
	}
}

func TestRestartBlockedByOpLock(t *testing.T) {
	h := defaultHarness()
	h.lockErr = &locks.HeldError{Service: "mysql-main", Lock: locks.Lock{Path: "/run/sermo/ops/mysql-main.lock"}}
	res := h.restart(t)
	if res.Status != ResultBlocked || res.Message != "operation in progress" {
		t.Fatalf("res = %+v, want blocked/operation in progress", res)
	}
	if h.mgr.did("stop mysqld") {
		t.Error("must not stop when the op lock is held")
	}
	if h.released != 0 {
		t.Errorf("must not release a lock it never acquired (released=%d)", h.released)
	}
	if len(res.Locks) != 1 {
		t.Errorf("blocked-by-lock result should carry the held lock")
	}
}

func TestOperationLockBlocksAllServiceActions(t *testing.T) {
	for _, action := range []string{"start", "stop", "restart", "reload", "resume"} {
		t.Run(action, func(t *testing.T) {
			h := defaultHarness()
			h.lockErr = &locks.HeldError{Service: "mysql-main", Lock: locks.Lock{Path: "/run/sermo/ops/mysql-main.lock"}}

			res := h.action(t, action)
			if res.Status != ResultBlocked || res.Message != "operation in progress" {
				t.Fatalf("res = %+v, want blocked/operation in progress", res)
			}
			if len(h.mgr.calls) != 0 {
				t.Fatalf("service action must not run while op lock is held; calls=%v", h.mgr.calls)
			}
			if h.released != 0 {
				t.Fatalf("must not release a lock it never acquired (released=%d)", h.released)
			}
			if len(res.Locks) != 1 {
				t.Fatalf("blocked-by-lock result should carry the held lock")
			}
		})
	}
}

func TestRestartBlockedByNamedLock(t *testing.T) {
	h := defaultHarness()
	h.named = []locks.Lock{{Service: "mysql-main", Name: "backup", State: locks.StateActive}}
	res := h.restart(t)
	if res.Status != ResultBlocked {
		t.Fatalf("status = %q, want blocked", res.Status)
	}
	if len(res.Locks) != 1 || res.Locks[0].Name != "backup" {
		t.Errorf("result should list the active named lock: %+v", res.Locks)
	}
	if h.mgr.did("stop mysqld") {
		t.Error("must not stop while a named lock is active")
	}
	if h.released != 1 {
		t.Errorf("op lock must be released (released=%d)", h.released)
	}
}

func TestNamedRuntimeLockBlocksAllServiceActions(t *testing.T) {
	for _, action := range []string{"start", "stop", "restart", "reload", "resume"} {
		t.Run(action, func(t *testing.T) {
			h := defaultHarness()
			h.named = []locks.Lock{{Service: "mysql-main", Name: "backup", State: locks.StateActive}}

			res := h.action(t, action)
			if res.Status != ResultBlocked || res.Message != "blocked by active runtime lock" {
				t.Fatalf("res = %+v, want blocked by active runtime lock", res)
			}
			if len(res.Locks) != 1 || res.Locks[0].Name != "backup" {
				t.Fatalf("result should list the active named lock: %+v", res.Locks)
			}
			if len(h.mgr.calls) != 0 {
				t.Fatalf("service action must not run while a named lock is active; calls=%v", h.mgr.calls)
			}
			if h.released != 1 {
				t.Fatalf("op lock must be released (released=%d)", h.released)
			}
		})
	}
}

func TestRestartIgnoresStaleNamedLock(t *testing.T) {
	h := defaultHarness()
	h.named = []locks.Lock{{Service: "mysql-main", State: locks.StateExpired}}
	if res := h.restart(t); !res.OK() {
		t.Fatalf("an expired named lock must not block: %+v", res)
	}
}

func TestReloadRunsPreflightThenReload(t *testing.T) {
	h := defaultHarness()
	res := h.engine().Reload(context.Background())
	if res.Status != ResultOK {
		t.Fatalf("status = %q, want ok", res.Status)
	}
	if !h.mgr.did("reload mysqld") {
		t.Errorf("reload must call Manager.Reload; calls=%v", h.mgr.calls)
	}
	if h.mgr.did("stop mysqld") || h.mgr.did("start mysqld") {
		t.Errorf("reload must not stop/start; calls=%v", h.mgr.calls)
	}
}

func TestReloadPreflightFailedDoesNotReload(t *testing.T) {
	h := defaultHarness()
	h.preflight = checks.Outcome{OK: false, Results: []checks.Result{{Check: "config", OK: false}}}
	res := h.engine().Reload(context.Background())
	if res.Status != ResultPreflightFailed {
		t.Fatalf("status = %q, want preflight_failed", res.Status)
	}
	if h.mgr.did("reload mysqld") {
		t.Error("must not reload when preflight (config) fails")
	}
}

func TestPreflightBlocksReloadAndResume(t *testing.T) {
	for _, action := range []string{"reload", "resume"} {
		t.Run(action, func(t *testing.T) {
			h := defaultHarness()
			h.preflight = checks.Outcome{OK: false, Results: []checks.Result{{Check: "config", OK: false}}}

			res := h.action(t, action)
			if res.Status != ResultPreflightFailed {
				t.Fatalf("status = %q, want preflight_failed", res.Status)
			}
			if len(h.mgr.calls) != 0 {
				t.Fatalf("must not run %s when preflight fails; calls=%v", action, h.mgr.calls)
			}
			if len(res.Checks) != 1 {
				t.Fatalf("result should carry the preflight check results")
			}
		})
	}
}

func TestReloadFailureSurfaces(t *testing.T) {
	h := defaultHarness()
	h.mgr.reloadErr = errors.New("no ExecReload")
	res := h.engine().Reload(context.Background())
	if res.Status != ResultFailed {
		t.Fatalf("status = %q, want failed", res.Status)
	}
}

func TestRestartPreflightFailed(t *testing.T) {
	h := defaultHarness()
	h.preflight = checks.Outcome{OK: false, Results: []checks.Result{{Check: "config", OK: false}}}
	res := h.restart(t)
	if res.Status != ResultPreflightFailed {
		t.Fatalf("status = %q, want preflight_failed", res.Status)
	}
	if h.mgr.did("stop mysqld") {
		t.Error("must not stop when preflight fails")
	}
	if len(res.Checks) != 1 {
		t.Errorf("result should carry the preflight check results")
	}
}

func TestRestartGuardBlocks(t *testing.T) {
	h := defaultHarness()
	h.guardBlocked = true
	h.guardReason = "backup running"
	res := h.restart(t)
	if res.Status != ResultBlocked || res.Message != "backup running" {
		t.Fatalf("res = %+v, want blocked/backup running", res)
	}
	if h.mgr.did("stop mysqld") {
		t.Error("must not stop when a guard blocks")
	}
}

func TestGuardBlocksReloadAndResume(t *testing.T) {
	for _, action := range []string{"reload", "resume"} {
		t.Run(action, func(t *testing.T) {
			h := defaultHarness()
			h.guardBlocked = true
			h.guardReason = "backup running"

			res := h.action(t, action)
			if res.Status != ResultBlocked || res.Message != "backup running" {
				t.Fatalf("res = %+v, want blocked/backup running", res)
			}
			if len(h.mgr.calls) != 0 {
				t.Fatalf("must not run %s when a guard blocks; calls=%v", action, h.mgr.calls)
			}
		})
	}
}

func TestRestartGuardErrorFailsSafe(t *testing.T) {
	h := defaultHarness()
	h.guardErr = errors.New("cannot evaluate")
	if res := h.restart(t); res.Status != ResultFailed {
		t.Fatalf("status = %q, want failed on guard error", res.Status)
	}
}

func TestRestartStopError(t *testing.T) {
	h := defaultHarness()
	h.mgr.stopErr = errors.New("unit refused")
	res := h.restart(t)
	if res.Status != ResultFailed {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if h.mgr.did("start mysqld") {
		t.Error("must not start after a failed stop")
	}
}

func TestRestartOrphanProcessesDoesNotStart(t *testing.T) {
	h := defaultHarness()
	// force_kill=false: residuals are returned as orphans untouched.
	h.discoverSteps = [][]process.Process{{{PID: 100, Exe: "/opt/x", ExeOK: true}}}
	h.killPolicy = process.KillPolicy{ForceKill: false}
	res := h.restart(t)
	if res.Status != ResultOrphanProcesses {
		t.Fatalf("status = %q, want orphan_processes", res.Status)
	}
	if h.mgr.did("start mysqld") {
		t.Error("must NOT start the service when residuals remain")
	}
	if len(res.Processes) != 1 {
		t.Errorf("result should list remaining residuals")
	}
}

func TestRestartDiscoveryErrorDoesNotStart(t *testing.T) {
	h := defaultHarness()
	h.discoverErrs = []error{errors.New("selector config: process selector has invalid cmd regex")}
	res := h.restart(t)
	if res.Status != ResultFailed {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if !strings.Contains(res.Message, "process discovery: selector config") {
		t.Fatalf("message = %q, want process discovery selector error", res.Message)
	}
	if h.mgr.did("start mysqld") {
		t.Error("must NOT start when residual discovery is unreliable")
	}
}

func TestRestartResidualsClearedThenStarts(t *testing.T) {
	h := defaultHarness()
	killable := process.Process{PID: 100, UID: 110, Exe: "/opt/x", ExeOK: true}
	// Initial discovery sees the residual; after SIGTERM rediscovery is empty.
	h.discoverSteps = [][]process.Process{{killable}, {}}
	h.killPolicy = process.KillPolicy{
		ForceKill:   true,
		KillOnlyIf:  process.KillSelector{Users: []string{"mysql"}, ExeAny: []string{"/opt/x"}},
		TermTimeout: time.Second,
	}
	h.reaper = process.Reaper{
		Signaler:    noopSignaler{},
		ResolveUser: func(string) (uint32, bool) { return 110, true },
		Sleep:       func(time.Duration) {},
	}
	res := h.restart(t)
	if !res.OK() {
		t.Fatalf("status = %q, want ok after residuals cleared (%s)", res.Status, res.Message)
	}
	if !h.mgr.did("start mysqld") {
		t.Error("should start once residuals are cleared")
	}
}

func TestRestartRediscoveryErrorDoesNotStart(t *testing.T) {
	h := defaultHarness()
	killable := process.Process{PID: 100, UID: 110, Exe: "/opt/x", ExeOK: true}
	h.discoverSteps = [][]process.Process{{killable}, {}}
	h.discoverErrs = []error{nil, errors.New("runtime discovery: pidfile missing")}
	h.killPolicy = process.KillPolicy{
		ForceKill:   true,
		KillOnlyIf:  process.KillSelector{Users: []string{"mysql"}, ExeAny: []string{"/opt/x"}},
		TermTimeout: time.Second,
	}
	h.reaper = process.Reaper{
		Signaler:    noopSignaler{},
		ResolveUser: func(string) (uint32, bool) { return 110, true },
		Sleep:       func(time.Duration) {},
	}
	res := h.restart(t)
	if res.Status != ResultFailed {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if !strings.Contains(res.Message, "process discovery: runtime discovery") {
		t.Fatalf("message = %q, want process discovery runtime error", res.Message)
	}
	if h.mgr.did("start mysqld") {
		t.Error("must NOT start when rediscovery fails")
	}
}

func TestNewInvalidReloadBlocksRestart(t *testing.T) {
	dir := t.TempDir()
	locker := locks.NewOperationLocker(filepath.Join(dir, "ops"))
	mgr := &fakeManager{status: servicemgr.StatusActive}
	engine := New(Config{
		Service:    "web",
		Unit:       "nginx",
		Backend:    "systemd",
		Tree:       map[string]any{"reload": map[string]any{"signal": "NOTASIGNAL"}},
		Manager:    mgr,
		Locker:     &locker,
		Scanner:    locks.NewScanner(filepath.Join(dir, "locks")),
		Discoverer: process.NewDiscovererWithUserLookup(nil),
		Sleep:      func(time.Duration) {},
	})

	res := engine.Restart(context.Background())
	if res.Status != ResultFailed {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if !strings.Contains(res.Message, "reload.signal") {
		t.Fatalf("message = %q, want reload config error", res.Message)
	}
	if mgr.did("start nginx") {
		t.Error("must NOT start when reload config is invalid")
	}
}

func TestNewInvalidProcessSelectorBlocksRestart(t *testing.T) {
	dir := t.TempDir()
	locker := locks.NewOperationLocker(filepath.Join(dir, "ops"))
	mgr := &fakeManager{status: servicemgr.StatusActive}
	engine := New(Config{
		Service:    "mysql-main",
		Unit:       "mysqld",
		Backend:    "systemd",
		Tree:       map[string]any{"processes": map[string]any{"main": map[string]any{"user": "mysql"}}},
		Manager:    mgr,
		Locker:     &locker,
		Scanner:    locks.NewScanner(filepath.Join(dir, "locks")),
		Discoverer: process.NewDiscovererWithUserLookup(nil),
		Sleep:      func(time.Duration) {},
	})

	res := engine.Restart(context.Background())
	if res.Status != ResultFailed {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if !strings.Contains(res.Message, "selector config") {
		t.Fatalf("message = %q, want selector config warning", res.Message)
	}
	if mgr.did("start mysqld") {
		t.Error("must NOT start when operation.New sees an invalid process selector")
	}
}

// countingPIDReader counts how often the live process table is walked, so a test
// can prove residual discovery bypasses the CachingReader's freshness window.
type countingPIDReader struct {
	ids   map[int]process.Identity
	walks int
}

func (r *countingPIDReader) PIDs() ([]int, error) {
	r.walks++
	pids := make([]int, 0, len(r.ids))
	for pid := range r.ids {
		pids = append(pids, pid)
	}
	return pids, nil
}

func (r *countingPIDReader) Identity(pid int) (process.Identity, bool) {
	id, ok := r.ids[pid]
	return id, ok
}

func TestNewResidualDiscoveryReadsLiveProcfs(t *testing.T) {
	// The residual-discovery closure must read live /proc on every call, never a
	// cached monitoring snapshot — otherwise the reaper would SIGKILL PIDs that
	// already exited (safety invariants 1, 4, 12). With a long freshness window a
	// plain CachingReader would walk /proc once; the operation engine must
	// invalidate it before each discovery.
	inner := &countingPIDReader{ids: map[int]process.Identity{100: {PID: 100, PPID: 1}}}
	discoverer := process.NewDiscovererWithUserLookup(nil)
	discoverer.Reader = process.NewCachingReader(inner, time.Hour)

	dir := t.TempDir()
	locker := locks.NewOperationLocker(filepath.Join(dir, "ops"))
	engine := New(Config{
		Service:    "mysql-main",
		Unit:       "mysqld",
		Backend:    "systemd",
		Tree:       map[string]any{"processes": map[string]any{"main": map[string]any{"exe": "/usr/sbin/mysqld", "user": "mysql"}}},
		Manager:    &fakeManager{status: servicemgr.StatusActive},
		Locker:     &locker,
		Scanner:    locks.NewScanner(filepath.Join(dir, "locks")),
		Discoverer: discoverer,
		Sleep:      func(time.Duration) {},
	})

	if _, err := engine.Discover(); err != nil {
		t.Fatalf("first Discover: %v", err)
	}
	if _, err := engine.Discover(); err != nil {
		t.Fatalf("second Discover: %v", err)
	}
	if inner.walks != 2 {
		t.Fatalf("live /proc walks = %d; want 2 (cache invalidated before each residual discovery)", inner.walks)
	}
}

func TestNewRuntimeDiscoveryWarningWithoutCommandMatchBlocksRestart(t *testing.T) {
	dir := t.TempDir()
	locker := locks.NewOperationLocker(filepath.Join(dir, "ops"))
	mgr := &fakeManager{status: servicemgr.StatusActive}
	engine := New(Config{
		Service: "mysql-main",
		Unit:    "mysqld",
		Backend: "systemd",
		Tree: map[string]any{
			"pidfile": filepath.Join(dir, "missing.pid"),
		},
		Manager:    mgr,
		Locker:     &locker,
		Scanner:    locks.NewScanner(filepath.Join(dir, "locks")),
		Discoverer: process.NewDiscovererWithUserLookup(nil),
		Sleep:      func(time.Duration) {},
	})

	res := engine.Restart(context.Background())
	if res.Status != ResultFailed {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if !strings.Contains(res.Message, "runtime discovery") {
		t.Fatalf("message = %q, want runtime discovery warning", res.Message)
	}
	if mgr.did("start mysqld") {
		t.Error("must NOT start when runtime discovery is unreliable")
	}
}

func TestNewRuntimeDiscoveryWarningWithCommandMatchDoesNotBlockRestart(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "mysqld")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	locker := locks.NewOperationLocker(filepath.Join(dir, "ops"))
	mgr := &fakeManager{status: servicemgr.StatusActive}
	engine := New(Config{
		Service: "mysql-main",
		Unit:    "mysqld",
		Backend: "systemd",
		Tree: map[string]any{
			"pidfile": filepath.Join(dir, "missing.pid"),
			"processes": map[string]any{
				"main": map[string]any{"exe": exe, "user": "mysql"},
			},
		},
		Manager: mgr,
		Locker:  &locker,
		Scanner: locks.NewScanner(filepath.Join(dir, "locks")),
		Discoverer: process.Discoverer{
			Reader:      &countingPIDReader{ids: map[int]process.Identity{}},
			ResolveUser: func(name string) (uint32, bool) { return 1001, name == "mysql" },
		},
		Sleep: func(time.Duration) {},
	})

	res := engine.Restart(context.Background())
	if res.Status != ResultOK {
		t.Fatalf("status = %q (%s), want ok", res.Status, res.Message)
	}
	if !mgr.did("start mysqld") {
		t.Fatalf("restart should proceed when an exact process selector can rediscover residuals; calls=%v", mgr.calls)
	}
}

func TestNewInvalidStopPolicyDurationBlocksBeforeServiceAction(t *testing.T) {
	dir := t.TempDir()
	locker := locks.NewOperationLocker(filepath.Join(dir, "ops"))
	mgr := &fakeManager{status: servicemgr.StatusActive}
	engine := New(Config{
		Service: "mysql-main",
		Unit:    "mysqld",
		Backend: "systemd",
		Tree: map[string]any{
			"stop_policy": map[string]any{"term_timeout": "notaduration"},
		},
		Manager:    mgr,
		Locker:     &locker,
		Scanner:    locks.NewScanner(filepath.Join(dir, "locks")),
		Discoverer: process.NewDiscovererWithUserLookup(nil),
		Sleep:      func(time.Duration) {},
	})

	res := engine.Restart(context.Background())
	if res.Status != ResultFailed {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if !strings.Contains(res.Message, "config: stop_policy") {
		t.Fatalf("message = %q, want stop_policy config error", res.Message)
	}
	if mgr.did("stop mysqld") || mgr.did("start mysqld") {
		t.Fatalf("must not call service manager with invalid stop_policy, calls=%v", mgr.calls)
	}
}

func TestNewWiresDefaultRuntimeDeps(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "mysqld")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	locker := locks.NewOperationLocker(filepath.Join(dir, "ops"))
	reader := &countingPIDReader{
		ids: map[int]process.Identity{
			200: {PID: 200, PPID: 1, UID: 1001, Exe: exe, ExeOK: true, State: "S"},
		},
	}
	discoverer := process.Discoverer{
		Reader:      reader,
		ResolveUser: func(name string) (uint32, bool) { return 1001, name == "mysql" },
	}
	mgr := &fakeManager{status: servicemgr.StatusActive}
	engine := New(Config{
		Service: "mysql-main",
		Unit:    "mysqld",
		Backend: "systemd",
		Tree: map[string]any{
			"preflight": map[string]any{
				"service": map[string]any{"type": "service", "expect": "active"},
				"process": map[string]any{"type": "process", "exe": exe, "user": "mysql", "state": "running"},
			},
		},
		Manager:    mgr,
		Locker:     &locker,
		Scanner:    locks.NewScanner(filepath.Join(dir, "locks")),
		Discoverer: discoverer,
		Sleep:      func(time.Duration) {},
	})

	if engine.LockTTL != 5*time.Minute {
		t.Fatalf("LockTTL = %v, want default 5m", engine.LockTTL)
	}
	res := engine.Start(context.Background())
	if res.Status != ResultOK {
		t.Fatalf("status = %q (%s), checks=%+v", res.Status, res.Message, res.Checks)
	}
	if !mgr.did("start mysqld") {
		t.Fatalf("start did not reach manager; calls=%v", mgr.calls)
	}
	if reader.walks == 0 {
		t.Fatal("process preflight did not use the Discoverer observer")
	}
}

func TestNewPreservesExplicitResolveUser(t *testing.T) {
	dir := t.TempDir()
	locker := locks.NewOperationLocker(filepath.Join(dir, "ops"))
	engine := New(Config{
		Service: "mysql-main",
		Unit:    "mysqld",
		Backend: "systemd",
		Tree:    map[string]any{},
		Manager: &fakeManager{status: servicemgr.StatusActive},
		Locker:  &locker,
		Scanner: locks.NewScanner(filepath.Join(dir, "locks")),
		Discoverer: process.Discoverer{
			ResolveUser: func(string) (uint32, bool) { return 7, true },
		},
		ResolveUser: func(name string) (uint32, bool) {
			return 42, name == "mysql"
		},
		Sleep: func(time.Duration) {},
	})

	uid, ok := engine.Reaper.ResolveUser("mysql")
	if !ok || uid != 42 {
		t.Fatalf("ResolveUser(mysql) = %d, %v; want explicit resolver uid 42", uid, ok)
	}
}

func TestRestartStartError(t *testing.T) {
	h := defaultHarness()
	h.mgr.startErr = errors.New("boom")
	if res := h.restart(t); res.Status != ResultFailed {
		t.Fatalf("status = %q, want failed", res.Status)
	}
}

func TestRestartServiceFailedAfterStart(t *testing.T) {
	h := defaultHarness()
	h.mgr.status = servicemgr.StatusFailed
	if res := h.restart(t); res.Status != ResultFailed {
		t.Fatalf("status = %q, want failed (service failed after start)", res.Status)
	}
}

func TestRestartPostflightFailed(t *testing.T) {
	h := defaultHarness()
	h.postflight = checks.Outcome{OK: false, Results: []checks.Result{{Check: "http", OK: false}}}
	res := h.restart(t)
	if res.Status != ResultPostflightFailed {
		t.Fatalf("status = %q, want postflight_failed", res.Status)
	}
	if !h.mgr.did("start mysqld") {
		t.Error("postflight runs after a successful start; the service stays up")
	}
}

func TestStopRunsNoPreflightOrStart(t *testing.T) {
	h := defaultHarness()
	h.preflight = checks.Outcome{OK: false} // would block restart, must be ignored by stop
	res := h.engine().Stop(context.Background())
	if !res.OK() {
		t.Fatalf("stop status = %q, want ok (%s)", res.Status, res.Message)
	}
	if h.mgr.did("start mysqld") {
		t.Error("stop must never start the service")
	}
	if !h.mgr.did("stop mysqld") {
		t.Error("stop must call backend stop")
	}
}

func TestStartRunsNoStop(t *testing.T) {
	h := defaultHarness()
	res := h.engine().Start(context.Background())
	if !res.OK() {
		t.Fatalf("start status = %q, want ok", res.Status)
	}
	if h.mgr.did("stop mysqld") {
		t.Error("start must not stop the service")
	}
	if !h.mgr.did("start mysqld") {
		t.Error("start must call backend start")
	}
}

type noopSignaler struct{}

func (noopSignaler) Signal(int, syscall.Signal) error { return nil }

func TestAlsoServiceRestartWrapOrder(t *testing.T) {
	h := defaultHarness()
	e := h.engine()
	e.AlsoUnits = []string{"docker.socket"}
	res := e.Restart(context.Background())
	if res.Status != ResultOK {
		t.Fatalf("status = %q (%s)", res.Status, res.Message)
	}
	// Wrap order: primary down first then also; also up first then primary.
	var seq []string
	for _, c := range h.mgr.calls {
		if c == "stop mysqld" || c == "stop docker.socket" || c == "start docker.socket" || c == "start mysqld" {
			seq = append(seq, c)
		}
	}
	want := []string{"stop mysqld", "stop docker.socket", "start docker.socket", "start mysqld"}
	if len(seq) != len(want) {
		t.Fatalf("call seq = %v, want %v", seq, want)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("call seq = %v, want %v", seq, want)
		}
	}
}

// With more than one also_service unit the teardown order matters: down in
// REVERSE declaration order (LIFO nesting), up in declaration order. A
// single-unit test cannot catch a forward-iteration regression.
func TestAlsoServiceMultiUnitWrapOrder(t *testing.T) {
	h := defaultHarness()
	e := h.engine()
	e.AlsoUnits = []string{"a.socket", "b.socket"}
	if res := e.Restart(context.Background()); res.Status != ResultOK {
		t.Fatalf("status = %q (%s)", res.Status, res.Message)
	}
	var seq []string
	for _, c := range h.mgr.calls {
		switch c {
		case "stop mysqld", "stop a.socket", "stop b.socket", "start a.socket", "start b.socket", "start mysqld":
			seq = append(seq, c)
		}
	}
	want := []string{"stop mysqld", "stop b.socket", "stop a.socket", "start a.socket", "start b.socket", "start mysqld"}
	if len(seq) != len(want) {
		t.Fatalf("call seq = %v, want %v", seq, want)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("call seq = %v, want %v", seq, want)
		}
	}
}

func TestAlsoServiceStartStrictAborts(t *testing.T) {
	h := defaultHarness()
	h.mgr.errOn = map[string]error{"start docker.socket": errors.New("socket down")}
	e := h.engine()
	e.AlsoUnits = []string{"docker.socket"}
	res := e.Restart(context.Background())
	if res.Status != ResultFailed {
		t.Fatalf("a failing also_service start must fail the op, got %q", res.Status)
	}
	if h.mgr.did("start mysqld") {
		t.Fatal("primary must NOT start when an also_service unit fails to start")
	}
}

func TestAlsoServiceStopBestEffort(t *testing.T) {
	h := defaultHarness()
	h.mgr.errOn = map[string]error{"stop docker.socket": errors.New("socket stuck")}
	e := h.engine()
	e.AlsoUnits = []string{"docker.socket"}
	res := e.Restart(context.Background())
	if res.Status != ResultOK {
		t.Fatalf("a failing also_service stop must not fail the op, got %q (%s)", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "docker.socket") {
		t.Fatalf("best-effort stop failure should be noted in the message, got %q", res.Message)
	}
	if !h.mgr.did("start mysqld") {
		t.Fatal("primary must still start after a best-effort also_service stop failure")
	}
}

func TestVerifyStoppedWarnsAndRemoves(t *testing.T) {
	dir := t.TempDir()
	pidf := filepath.Join(dir, "svc.pid")
	sock := filepath.Join(dir, "app.sock")
	for _, f := range []string{pidf, sock} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// report-only: a clean stop warns about the lingering pidfile + socket glob.
	h := defaultHarness()
	e := h.engine()
	e.StopArtifacts = StopArtifacts{PidfilePaths: []string{pidf}, Files: []string{filepath.Join(dir, "*.sock")}}
	res := e.Stop(context.Background())
	if res.Status != ResultOK || !strings.Contains(res.Message, "stale") {
		t.Fatalf("report-only stale artifact must warn (OK + 'stale'), got %q (%s)", res.Status, res.Message)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatal("report-only must NOT remove the file")
	}
	// remove: the same stop deletes the stale files.
	h2 := defaultHarness()
	e2 := h2.engine()
	e2.StopArtifacts = StopArtifacts{PidfilePaths: []string{pidf}, Files: []string{filepath.Join(dir, "*.sock")}, CleanEnabled: true}
	res2 := e2.Stop(context.Background())
	if res2.Status != ResultOK {
		t.Fatalf("remove stop status = %q (%s)", res2.Status, res2.Message)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatal("remove:true must delete the stale socket")
	}
}

func TestCleanOnStopDeletesFilesAndDirs(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "foo.tmp")
	subdir := filepath.Join(dir, "work")
	nested := filepath.Join(subdir, "a", "b.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	clean := []CleanPath{
		{Path: file},                    // plain file
		{Path: subdir, Recursive: true}, // non-empty dir tree
	}

	// clean_after_stop off (default): the list is inert — nothing is deleted.
	h0 := defaultHarness()
	e0 := h0.engine()
	e0.StopArtifacts = StopArtifacts{Clean: clean}
	if res := e0.Stop(context.Background()); res.Status != ResultOK {
		t.Fatalf("clean_on_stop (disabled) status = %q (%s)", res.Status, res.Message)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("clean_on_stop must NOT delete when clean_after_stop is off")
	}
	if _, err := os.Stat(subdir); err != nil {
		t.Fatal("clean_on_stop must NOT delete the dir when clean_after_stop is off")
	}

	// clean_after_stop on: the list is deleted (file and recursive dir tree).
	h := defaultHarness()
	e := h.engine()
	e.StopArtifacts = StopArtifacts{CleanEnabled: true, Clean: clean}
	res := e.Stop(context.Background())
	if res.Status != ResultOK {
		t.Fatalf("clean_on_stop status = %q (%s)", res.Status, res.Message)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("clean_on_stop must delete the file")
	}
	if _, err := os.Stat(subdir); !os.IsNotExist(err) {
		t.Fatal("recursive clean_on_stop must delete the directory tree")
	}
}
