package operation

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"

	"sermo/internal/locks"
	"sermo/internal/process"
)

// reapSignaler records the exact (pid, signal) pairs a reap delivered, which the
// shared recordingSignaler flattens into strings and cannot fail on demand.
type reapSignaler struct {
	calls []reapSignal
	err   error
}

type reapSignal struct {
	pid int
	sig syscall.Signal
}

func (s *reapSignaler) Signal(pid int, sig syscall.Signal) error {
	s.calls = append(s.calls, reapSignal{pid: pid, sig: sig})
	return s.err
}

func strayProc(pid int, exe string) process.Process {
	return process.Process{
		PID: pid, PPID: 1, UID: 0, User: "root",
		Exe: exe, ExeOK: true,
		Role: process.RoleMain, Source: process.SourceBackend, Stray: true,
	}
}

func reapResolveUser(name string) (uint32, bool) {
	if name == "root" {
		return 0, true
	}
	return 0, false
}

// reapEngine wires a harness engine with the reap selector and signaler under
// test. selector nil means the service declared no `reap:` block.
func reapEngine(h *harness, signaler process.Signaler, selector *process.KillSelector) Engine {
	h.reaper = process.Reaper{Signaler: signaler, ResolveUser: reapResolveUser}
	e := h.engine()
	if selector != nil {
		e.ReapSelector = *selector
	}
	return e
}

func dbusReapSelector() process.KillSelector {
	selector, warnings := process.ParseReapPolicy(map[string]any{
		process.SectionReap: map[string]any{
			process.ReapKeyKillOnlyIf: map[string]any{
				process.ReapKeyUsers:  []any{"root"},
				process.ReapKeyExeAny: []any{"/usr/bin/dbus-daemon"},
			},
		},
	})
	if len(warnings) > 0 {
		panic(strings.Join(warnings, "; "))
	}
	return selector
}

// The preview is a read: it must not take the operation lock, must not emit an
// event, and must not touch a single process.
func TestReapPreviewIsReadOnly(t *testing.T) {
	h := defaultHarness()
	h.discoverSteps = [][]process.Process{{
		strayProc(300, "/usr/bin/dbus-daemon"),
		strayProc(400, "/usr/bin/other"),
	}}
	signaler := &reapSignaler{}
	selector := dbusReapSelector()

	res := reapEngine(h, signaler, &selector).Reap(context.Background(), false)

	if res.Status != ResultOK {
		t.Fatalf("status = %q, want %q", res.Status, ResultOK)
	}
	if want := "preview: 1 of 2 stray process(es) would be signalled"; res.Message != want {
		t.Fatalf("message = %q, want %q", res.Message, want)
	}
	if len(res.Processes) != 2 {
		t.Fatalf("processes = %d, want both strays listed", len(res.Processes))
	}
	if len(signaler.calls) != 0 {
		t.Fatalf("preview signalled %v", signaler.calls)
	}
	if len(h.emitted) != 0 {
		t.Fatalf("preview emitted %d event(s), want none", len(h.emitted))
	}
	if h.released != 0 {
		t.Fatal("preview must not acquire the operation lock")
	}
}

func TestReapPreviewWithoutStrays(t *testing.T) {
	h := defaultHarness()
	res := reapEngine(h, &reapSignaler{}, nil).Reap(context.Background(), false)

	if res.Status != ResultOK || res.Message != "no stray processes" {
		t.Fatalf("result = %+v, want ok with no strays", res)
	}
}

func TestReapPreviewReportsConfigError(t *testing.T) {
	h := defaultHarness()
	e := reapEngine(h, &reapSignaler{}, nil)
	e.ConfigError = errors.New("reap: kill_if is not supported")

	res := e.Reap(context.Background(), false)
	if res.Status != ResultFailed || !strings.Contains(res.Message, "kill_if") {
		t.Fatalf("result = %+v, want the config error", res)
	}
}

// The fail-safe: with no `reap:` block the action reports every stray and signals
// none, and says why.
func TestReapApplyWithoutAuthorizationRefuses(t *testing.T) {
	h := defaultHarness()
	h.discoverSteps = [][]process.Process{{strayProc(300, "/usr/bin/dbus-daemon")}}
	signaler := &reapSignaler{}

	res := reapEngine(h, signaler, nil).Reap(context.Background(), true)

	if res.Status != ResultBlocked {
		t.Fatalf("status = %q, want %q", res.Status, ResultBlocked)
	}
	if !strings.Contains(res.Message, process.SectionReap+"."+process.ReapKeyKillOnlyIf) {
		t.Fatalf("message = %q, want it to name the missing authorization", res.Message)
	}
	if len(res.Processes) != 1 {
		t.Fatalf("processes = %d, want the stray reported", len(res.Processes))
	}
	if len(signaler.calls) != 0 {
		t.Fatalf("refused reap signalled %v", signaler.calls)
	}
	if len(h.emitted) != 1 {
		t.Fatalf("emitted %d event(s), want exactly 1", len(h.emitted))
	}
}

func TestReapApplySignalsAuthorizedStrays(t *testing.T) {
	h := defaultHarness()
	h.discoverSteps = [][]process.Process{{strayProc(300, "/usr/bin/dbus-daemon")}, {}}
	signaler := &reapSignaler{}
	selector := dbusReapSelector()

	res := reapEngine(h, signaler, &selector).Reap(context.Background(), true)

	if res.Status != ResultOK {
		t.Fatalf("status = %q (%s), want %q", res.Status, res.Message, ResultOK)
	}
	if want := "reap ok (signalled 1 of 1 stray process(es))"; res.Message != want {
		t.Fatalf("message = %q, want %q", res.Message, want)
	}
	if len(signaler.calls) != 1 || signaler.calls[0].pid != 300 || signaler.calls[0].sig != syscall.SIGTERM {
		t.Fatalf("signals = %+v, want one SIGTERM to 300", signaler.calls)
	}
	if len(h.emitted) != 1 {
		t.Fatalf("emitted %d event(s), want exactly 1", len(h.emitted))
	}
}

// A stray the selector does not name survives the reap and is reported, so the
// operation is honest about what it left behind.
func TestReapApplyReportsUnauthorizedSurvivor(t *testing.T) {
	h := defaultHarness()
	survivor := strayProc(400, "/usr/bin/other")
	h.discoverSteps = [][]process.Process{
		{strayProc(300, "/usr/bin/dbus-daemon"), survivor},
		{survivor},
		{survivor},
	}
	signaler := &reapSignaler{}
	selector := dbusReapSelector()

	res := reapEngine(h, signaler, &selector).Reap(context.Background(), true)

	if res.Status != ResultOrphanProcesses {
		t.Fatalf("status = %q (%s), want %q", res.Status, res.Message, ResultOrphanProcesses)
	}
	if !strings.Contains(res.Message, "1 remain") {
		t.Fatalf("message = %q, want it to count the survivor", res.Message)
	}
	if len(res.Processes) != 1 || res.Processes[0].PID != 400 {
		t.Fatalf("processes = %+v, want only pid 400", res.Processes)
	}
	for _, call := range signaler.calls {
		if call.pid == 400 {
			t.Fatal("an unauthorized stray must never be signalled")
		}
	}
}

// A delegated process is the service's workload, kept alive on purpose. It must
// never enter a reap even if classification and the selector both point at it.
func TestReapApplySkipsDelegated(t *testing.T) {
	h := defaultHarness()
	delegated := strayProc(300, "/usr/bin/dbus-daemon")
	delegated.Delegated = true
	h.discoverSteps = [][]process.Process{{delegated}}
	signaler := &reapSignaler{}
	selector := dbusReapSelector()

	res := reapEngine(h, signaler, &selector).Reap(context.Background(), true)

	if res.Message != "reap ok (no stray processes)" {
		t.Fatalf("message = %q, want the delegated process excluded entirely", res.Message)
	}
	if len(signaler.calls) != 0 {
		t.Fatalf("delegated process was signalled: %v", signaler.calls)
	}
}

func TestReapApplyReportsDiscoveryFailure(t *testing.T) {
	h := defaultHarness()
	h.discoverErrs = []error{errors.New("list process IDs: permission denied")}
	selector := dbusReapSelector()

	res := reapEngine(h, &reapSignaler{}, &selector).Reap(context.Background(), true)

	if res.Status != ResultFailed || !strings.Contains(res.Message, "permission denied") {
		t.Fatalf("result = %+v, want the discovery failure", res)
	}
}

func TestReapApplyReportsSignalFailure(t *testing.T) {
	h := defaultHarness()
	h.discoverSteps = [][]process.Process{{strayProc(300, "/usr/bin/dbus-daemon")}, {}}
	signaler := &reapSignaler{err: errors.New("operation not permitted")}
	selector := dbusReapSelector()

	res := reapEngine(h, signaler, &selector).Reap(context.Background(), true)

	if res.Status != ResultFailed {
		t.Fatalf("status = %q (%s), want %q", res.Status, res.Message, ResultFailed)
	}
	if !strings.Contains(res.Message, "pid 300") || !strings.Contains(res.Message, "not permitted") {
		t.Fatalf("message = %q, want the failing pid and reason", res.Message)
	}
}

// An active named runtime lock is a maintenance window; signalling anything
// inside it is exactly what the lock exists to prevent.
func TestReapApplyBlockedByNamedLock(t *testing.T) {
	h := defaultHarness()
	h.discoverSteps = [][]process.Process{{strayProc(300, "/usr/bin/dbus-daemon")}}
	h.named = []locks.Lock{{Name: "backup", Reason: "nightly dump", State: locks.StateActive}}
	signaler := &reapSignaler{}
	selector := dbusReapSelector()

	res := reapEngine(h, signaler, &selector).Reap(context.Background(), true)

	if res.Status != ResultBlocked {
		t.Fatalf("status = %q, want %q", res.Status, ResultBlocked)
	}
	if len(signaler.calls) != 0 {
		t.Fatalf("signalled during an active lock: %v", signaler.calls)
	}
}

// A stray survives a stop precisely because nothing in the configuration accounts
// for it, so "residual process" alone sends the operator looking for a selector
// that was never written. The stop result must name it and the verb that clears it.
func TestStopNamesStrayResidualsAndTheVerbThatClearsThem(t *testing.T) {
	h := defaultHarness()
	survivor := strayProc(4711, "/usr/bin/tmux")
	claimed := process.Process{PID: 4712, User: "root", Exe: "/usr/sbin/sshd", ExeOK: true, Source: process.SourceBackend}
	h.discoverSteps = [][]process.Process{{survivor, claimed}}

	res := h.action(t, "stop")

	if res.Status != ResultOrphanProcesses {
		t.Fatalf("status = %q (%s), want %q", res.Status, res.Message, ResultOrphanProcesses)
	}
	for _, want := range []string{"2 residual process(es) remain after stop", "1 stray", "sermoctl reap", process.ReapKeyKillOnlyIf} {
		if !strings.Contains(res.Message, want) {
			t.Fatalf("message %q must contain %q", res.Message, want)
		}
	}
}

// With no stray among them the wording stays exactly as it was: a residual that a
// selector does name is a different problem, with a different fix.
func TestStopResidualsWithoutStraysKeepTheirWording(t *testing.T) {
	h := defaultHarness()
	h.discoverSteps = [][]process.Process{{
		{PID: 4712, User: "root", Exe: "/usr/sbin/sshd", ExeOK: true, Source: process.SourceBackend},
	}}

	res := h.action(t, "stop")

	if want := "1 residual process(es) remain after stop"; res.Message != want {
		t.Fatalf("message = %q, want exactly %q", res.Message, want)
	}
}

// Reap is manual-only: no rule action name reaches it, so Do must not know it.
func TestReapIsNotDispatchableAsAnAction(t *testing.T) {
	h := defaultHarness()
	res := reapEngine(h, &reapSignaler{}, nil).Do(context.Background(), "reap")

	if res.Status != ResultFailed || !strings.Contains(res.Message, "unknown action") {
		t.Fatalf("result = %+v, want an unknown-action failure", res)
	}
}
