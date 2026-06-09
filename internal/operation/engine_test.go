package operation

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/locks"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

type fakeManager struct {
	stopErr   error
	startErr  error
	resetErr  error
	status    servicemgr.Status
	statusErr error
	calls     []string
}

func (m *fakeManager) Start(_ context.Context, s string) error {
	m.calls = append(m.calls, "start "+s)
	return m.startErr
}

func (m *fakeManager) Stop(_ context.Context, s string) error {
	m.calls = append(m.calls, "stop "+s)
	return m.stopErr
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
		Discover:   h.discover,
		Reaper:     h.reaper,
		KillPolicy: h.killPolicy,
		Sleep:      func(time.Duration) {},
		Emit:       func(r Result) { h.emitted = append(h.emitted, r) },
	}
}

func (h *harness) restart(t *testing.T) Result {
	t.Helper()
	res := h.engine().Restart(context.Background())
	if len(h.emitted) != 1 {
		t.Fatalf("Emit called %d times, want exactly 1", len(h.emitted))
	}
	if h.emitted[0].Status != res.Status {
		t.Fatalf("emitted result %q != returned %q", h.emitted[0].Status, res.Status)
	}
	return res
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
		t.Fatalf("status = %q, want orphan-processes", res.Status)
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

func TestRestartIgnoresStaleNamedLock(t *testing.T) {
	h := defaultHarness()
	h.named = []locks.Lock{{Service: "mysql-main", State: locks.StateExpired}}
	if res := h.restart(t); !res.OK() {
		t.Fatalf("an expired named lock must not block: %+v", res)
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
	h.discoverErrs = []error{errors.New("selector config: command_match selector requires both exe and user")}
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

func TestNewInvalidProcessSelectorBlocksRestart(t *testing.T) {
	dir := t.TempDir()
	locker := locks.NewOperationLocker(filepath.Join(dir, "ops"))
	mgr := &fakeManager{status: servicemgr.StatusActive}
	engine := New(Config{
		Service:    "mysql-main",
		Unit:       "mysqld",
		Backend:    "systemd",
		Tree:       map[string]any{"processes": map[string]any{"main": map[string]any{"type": "command_match", "user": "mysql"}}},
		Manager:    mgr,
		Locker:     &locker,
		Scanner:    locks.NewScanner(filepath.Join(dir, "locks")),
		Discoverer: process.NewDiscoverer(),
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

func TestNewRuntimeDiscoveryWarningWithoutCommandMatchBlocksRestart(t *testing.T) {
	dir := t.TempDir()
	locker := locks.NewOperationLocker(filepath.Join(dir, "ops"))
	mgr := &fakeManager{status: servicemgr.StatusActive}
	engine := New(Config{
		Service: "mysql-main",
		Unit:    "mysqld",
		Backend: "systemd",
		Tree: map[string]any{
			"processes": map[string]any{
				"pid": map[string]any{"type": "pidfile", "path": filepath.Join(dir, "missing.pid")},
			},
		},
		Manager:    mgr,
		Locker:     &locker,
		Scanner:    locks.NewScanner(filepath.Join(dir, "locks")),
		Discoverer: process.NewDiscoverer(),
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
		Discoverer: process.NewDiscoverer(),
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
