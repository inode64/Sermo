package operation

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

// Fixed identities keep the reconciliation fixtures and signal assertions in
// lockstep.
const (
	reconcileExe         = "/opt/mysqld"
	reconcileOrphanPID   = 4129
	reconcileWorkloadPID = 4644
	reconcileUID         = 1001
)

// orphanedDaemon is the service's own main process, still running after the init
// lost track of it.
func orphanedDaemon() process.Process {
	return process.Process{
		PID: reconcileOrphanPID, UID: reconcileUID, Exe: reconcileExe, ExeOK: true,
		Role: process.RoleMain, Source: process.SourceBackend,
	}
}

// delegatedWorkload is a process the unit deliberately keeps alive across a
// daemon restart, so nothing on the operation path may ever signal it.
func delegatedWorkload() process.Process {
	return process.Process{
		PID: reconcileWorkloadPID, UID: reconcileUID, Exe: reconcileExe, ExeOK: true,
		Role: "brick", Source: process.SourceBackend, Delegated: true,
	}
}

// divergentHarness builds the state this reconciliation exists for: the init has
// lost track of a live daemon — the unit reads failed with no MainPID — while the
// daemon it used to own is still holding its port and its delegated workload is
// still serving. A native restart cannot recover from this on its own, because it
// asks the init to signal a PID the init no longer knows and the replacement then
// collides with the survivor.
func divergentHarness() (*harness, *recordingSignaler) {
	h := defaultHarness()
	// Failed when the restart begins, active once the backend action has run.
	h.mgr.statusSteps = []servicemgr.Status{servicemgr.StatusFailed}
	h.mgr.status = servicemgr.StatusActive

	orphan, workload := orphanedDaemon(), delegatedWorkload()
	h.discoverSteps = [][]process.Process{
		{orphan, workload}, // initial authoritative residual discovery
		{workload},         // after SIGTERM, only the delegated workload remains
	}

	signaler := &recordingSignaler{}
	h.killPolicy = process.KillPolicy{
		ForceKill:  true,
		KillOnlyIf: process.KillSelector{Users: []string{"mysql"}, ExeAny: []string{reconcileExe}},
	}
	h.reaper = process.Reaper{
		Signaler:    signaler,
		ResolveUser: func(string) (uint32, bool) { return reconcileUID, true },
		Sleep:       func(time.Duration) {},
	}
	return h, signaler
}

func TestNativeRestartReconcilesStaleInitState(t *testing.T) {
	t.Parallel()

	h, signaler := divergentHarness()
	res := nativeRestartEngine(h).Restart(context.Background())

	if !res.OK() {
		t.Fatalf("status = %q, want ok (%s)", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "reconciled stale init state") {
		t.Fatalf("message = %q, want it to report the reconciliation", res.Message)
	}
	if !h.mgr.did("reset mysqld") {
		t.Fatalf("a reconciled unit must have its init state reset, calls=%v", h.mgr.calls)
	}
	if !h.mgr.did("restart mysqld") {
		t.Fatalf("reconciliation must not replace the restart itself, calls=%v", h.mgr.calls)
	}
	if h.discoverCalls != 2 {
		t.Fatalf("process discoveries = %d, want 2 (initial plus post-SIGTERM revalidation)", h.discoverCalls)
	}
	// The orphaned daemon is signalled; the workload the unit deliberately kept
	// alive is not. Signalling it would take the node's storage down with it.
	orphanCallPrefix := strconv.Itoa(reconcileOrphanPID) + " "
	for _, call := range signaler.calls {
		if !strings.HasPrefix(call, orphanCallPrefix) {
			t.Fatalf("signalled a process other than the orphaned daemon: %v", signaler.calls)
		}
	}
	if len(signaler.calls) == 0 {
		t.Fatalf("the orphaned daemon was never signalled, so nothing was reconciled")
	}
}

type restartModeCase struct {
	name   string
	engine func(*harness) Engine
}

func restartModeCases() []restartModeCase {
	return []restartModeCase{
		{name: "staged", engine: func(h *harness) Engine { return h.engine() }},
		{name: "native", engine: nativeRestartEngine},
	}
}

// The staged path reaches the same place through its own stop phase, so the
// reconciliation must be idempotent there rather than a second, conflicting stop.
func TestStagedRestartReconcilesStaleInitState(t *testing.T) {
	t.Parallel()

	h, _ := divergentHarness()
	res := h.engine().Restart(context.Background())

	if !res.OK() {
		t.Fatalf("status = %q, want ok (%s)", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "reconciled stale init state") {
		t.Fatalf("message = %q, want it to report the reconciliation", res.Message)
	}
	if !h.mgr.did("stop mysqld") || !h.mgr.did("start mysqld") {
		t.Fatalf("staged restart must keep its stop/start phases, calls=%v", h.mgr.calls)
	}
}

// A healthy unit must not pay for this: no discovery, no reset, no message.
func TestRestartSkipsReconciliationWhenUnitIsActive(t *testing.T) {
	t.Parallel()

	h, _ := divergentHarness()
	h.mgr.statusSteps = nil // active from the first probe

	res := nativeRestartEngine(h).Restart(context.Background())

	if !res.OK() {
		t.Fatalf("status = %q, want ok (%s)", res.Status, res.Message)
	}
	if strings.Contains(res.Message, "reconciled") {
		t.Fatalf("message = %q, want no reconciliation on an active unit", res.Message)
	}
	if h.mgr.did("reset mysqld") {
		t.Fatalf("an active unit needs no init-state reset, calls=%v", h.mgr.calls)
	}
	if h.discoverCalls != 0 {
		t.Fatalf("residual discovery calls = %d, want 0 on an active unit", h.discoverCalls)
	}
}

// Unknown includes transitional backend states such as systemd activating or
// deactivating. They are not proof that init lost a live daemon, so the restart
// may use its backend action but must never enter the residual reaper.
func TestRestartSkipsReconciliationWhenUnitStatusIsUnknown(t *testing.T) {
	t.Parallel()

	h, signaler := divergentHarness()
	h.mgr.statusSteps = []servicemgr.Status{servicemgr.StatusUnknown}
	h.mgr.status = servicemgr.StatusActive

	res := nativeRestartEngine(h).Restart(context.Background())

	if !res.OK() {
		t.Fatalf("status = %q, want ok (%s)", res.Status, res.Message)
	}
	if !h.mgr.did("restart mysqld") {
		t.Fatalf("the backend restart must still run, calls=%v", h.mgr.calls)
	}
	if h.discoverCalls != 0 || len(signaler.calls) != 0 {
		t.Fatalf("unknown state entered reconciliation: discovery=%d signals=%v", h.discoverCalls, signaler.calls)
	}
	if h.mgr.did("reset mysqld") {
		t.Fatalf("unknown state must not be reset, calls=%v", h.mgr.calls)
	}
}

// A backend restart or staged start can launch a second daemon because init no
// longer owns the survivor. Both modes must fail closed before any backend
// action when reconciliation leaves an unkillable residual.
func TestRestartBlocksWhenReconciliationLeavesResidual(t *testing.T) {
	t.Parallel()

	for _, mode := range restartModeCases() {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()

			h, signaler := divergentHarness()
			h.killPolicy = process.KillPolicy{}

			res := mode.engine(h).Restart(context.Background())

			if res.Status != ResultOrphanProcesses {
				t.Fatalf("status = %q (%s), want %q", res.Status, res.Message, ResultOrphanProcesses)
			}
			if len(res.Processes) != 1 || res.Processes[0].PID != orphanedDaemon().PID {
				t.Fatalf("processes = %+v, want only the non-delegated survivor", res.Processes)
			}
			if len(h.mgr.calls) != 0 {
				t.Fatalf("residual reconciliation reached the backend, calls=%v", h.mgr.calls)
			}
			if len(signaler.calls) != 0 {
				t.Fatalf("force_kill=false signalled residuals: %v", signaler.calls)
			}
			if len(h.emitted) != 1 || h.emitted[0].Status != ResultOrphanProcesses {
				t.Fatalf("emitted = %+v, want one orphan_processes result", h.emitted)
			}
		})
	}
}

func TestRestartBlocksWhenReconciliationFails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(*harness)
		message string
	}{
		{
			name: "init status query",
			setup: func(h *harness) {
				h.mgr.statusErr = errors.New("status unavailable")
			},
			message: "query init state before restart: status unavailable",
		},
		{
			name: "initial discovery",
			setup: func(h *harness) {
				h.discoverErrs = []error{errors.New("proc unavailable")}
			},
			message: "process discovery: proc unavailable",
		},
		{
			name: "residual rediscovery",
			setup: func(h *harness) {
				h.discoverErrs = []error{nil, errors.New("proc refresh failed")}
			},
			message: "process discovery: proc refresh failed",
		},
		{
			name: "init state reset",
			setup: func(h *harness) {
				h.mgr.resetErr = errors.New("reset refused")
			},
			message: "reset init state before restart: reset refused",
		},
	}
	for _, mode := range restartModeCases() {
		for _, tt := range tests {
			t.Run(mode.name+"/"+tt.name, func(t *testing.T) {
				t.Parallel()

				h, _ := divergentHarness()
				tt.setup(h)

				res := mode.engine(h).Restart(context.Background())

				if res.Status != ResultFailed || res.Message != tt.message {
					t.Fatalf("result = %+v, want failed with %q", res, tt.message)
				}
				for _, call := range []string{"stop mysqld", "start mysqld", "restart mysqld"} {
					if h.mgr.did(call) {
						t.Fatalf("reconciliation failure reached backend action %q, calls=%v", call, h.mgr.calls)
					}
				}
				if len(h.emitted) != 1 || h.emitted[0].Status != ResultFailed {
					t.Fatalf("emitted = %+v, want one failed result", h.emitted)
				}
			})
		}
	}
}

// A stopped service is not a divergence: the unit is inactive and nothing of it
// is running, which is exactly what a restart after a clean stop looks like.
func TestRestartSkipsReconciliationWithoutSurvivingProcesses(t *testing.T) {
	t.Parallel()

	h, signaler := divergentHarness()
	h.discoverSteps = [][]process.Process{nil}

	res := nativeRestartEngine(h).Restart(context.Background())

	if !res.OK() {
		t.Fatalf("status = %q, want ok (%s)", res.Status, res.Message)
	}
	if strings.Contains(res.Message, "reconciled") {
		t.Fatalf("message = %q, want no reconciliation without survivors", res.Message)
	}
	if h.mgr.did("reset mysqld") {
		t.Fatalf("an inactive unit with no processes needs no reset, calls=%v", h.mgr.calls)
	}
	if len(signaler.calls) != 0 {
		t.Fatalf("nothing should have been signalled, calls=%v", signaler.calls)
	}
}

// A unit whose only survivors are delegated is not diverging either: the init
// stopped what it owns, and the workload is meant to outlive it.
func TestRestartSkipsReconciliationWhenOnlySurvivorsAreDelegated(t *testing.T) {
	t.Parallel()

	h, signaler := divergentHarness()
	h.discoverSteps = [][]process.Process{{delegatedWorkload()}}

	res := nativeRestartEngine(h).Restart(context.Background())

	if !res.OK() {
		t.Fatalf("status = %q, want ok (%s)", res.Status, res.Message)
	}
	if strings.Contains(res.Message, "reconciled") {
		t.Fatalf("message = %q, want no reconciliation for a delegated-only survivor", res.Message)
	}
	if len(signaler.calls) != 0 {
		t.Fatalf("a delegated process must never be signalled, calls=%v", signaler.calls)
	}
}
