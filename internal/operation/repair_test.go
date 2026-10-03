package operation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/process"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
)

type repairReader map[int]bool

func (r repairReader) PIDs() ([]int, error) {
	pids := make([]int, 0, len(r))
	for pid, alive := range r {
		if alive {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// partialSnapshotReader is the daemon's caching reader after a /proc walk that
// failed part-way: the live PID is missing and the error says why.
type partialSnapshotReader struct{ repairReader }

func (partialSnapshotReader) SnapshotWithError() (map[int]process.Identity, error) {
	return map[int]process.Identity{}, errors.New("read process identity: permission denied")
}

// identityErrReader fails the direct read of one live PID.
type identityErrReader struct{ repairReader }

func (identityErrReader) IdentityWithError(pid int) (process.Identity, bool, error) {
	return process.Identity{}, false, fmt.Errorf("cannot read identity of live pid %d", pid)
}

func TestRepairKeepsPIDFileWhenAbsenceIsUnproven(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reader process.Reader
	}{
		{name: "incomplete snapshot", reader: partialSnapshotReader{}},
		{name: "unreadable pid", reader: identityErrReader{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtimeDir := filepath.Join(t.TempDir(), "run")
			if err := os.Mkdir(runtimeDir, 0o755); err != nil {
				t.Fatal(err)
			}
			pidfile := filepath.Join(runtimeDir, "rabbitmq.pid")
			if err := os.WriteFile(pidfile, []byte("5023\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			mgr := &fakeManager{status: servicemgr.StatusFailed}
			prepare := repairStalePIDFiles(mgr, "rabbitmq", []process.Selector{{
				Name: process.SelectorPidfile, Type: process.SelectorPidfile, Paths: []string{pidfile},
			}}, tc.reader, runtimeDir)

			_, err := prepare(context.Background())

			if err == nil || !strings.Contains(err.Error(), "cannot prove pid 5023 is gone") {
				t.Fatalf("repair error = %v, want fail-closed refusal", err)
			}
			if _, err := os.Lstat(pidfile); err != nil {
				t.Fatalf("pidfile must remain when absence is unproven: %v", err)
			}
		})
	}
}

func (r repairReader) Identity(pid int) (process.Identity, bool) {
	if !r[pid] {
		return process.Identity{}, false
	}
	return process.Identity{PID: pid}, true
}

func TestRepairIsManualOnly(t *testing.T) {
	action := rules.ActionType(ActionRepair)
	if action.IsOperation() || action.SettlesAfter() || action.CanRemainActiveAfterPostflightFailure() {
		t.Fatalf("repair must not be classified as a rule operation")
	}
	if strings.Contains(rules.RuleActionSummary, ActionRepair) {
		t.Fatalf("repair must not be listed as a rule action: %q", rules.RuleActionSummary)
	}
}

func TestRepairPIDFilePathsDeduplicatesAllSelectorsInDeclarationOrder(t *testing.T) {
	first := "/run/first.pid"
	second := "/run/second.pid"
	third := "/run/third.pid"
	paths := repairPIDFilePaths([]process.Selector{
		{Type: process.SelectorPidfile, Paths: []string{first, second}},
		{Type: process.SelectorCommandMatch, Paths: []string{"ignored"}},
		{Type: process.SelectorPidfile, Paths: []string{third, first, second}},
	})
	want := []string{first, second, third}
	if !slices.Equal(paths, want) {
		t.Fatalf("repair PID file paths = %v, want %v", paths, want)
	}
}

func TestRepairRemovesProvenStaleRuntimePIDFileThenStarts(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pidfile := filepath.Join(runtimeDir, "rabbitmq.pid")
	if err := os.WriteFile(pidfile, []byte("5023\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := defaultHarness()
	h.mgr.status = servicemgr.StatusActive
	// Failed through the repair precondition, the residual reconciliation and
	// the reset decision; reset then reports inactive and start reports active.
	h.mgr.statusSteps = []servicemgr.Status{servicemgr.StatusFailed, servicemgr.StatusFailed, servicemgr.StatusFailed}
	e := h.engine()
	e.RepairStalePIDFiles = repairStalePIDFiles(h.mgr, e.Unit, []process.Selector{{
		Name: process.SelectorPidfile, Type: process.SelectorPidfile, Paths: []string{pidfile},
	}}, repairReader{}, runtimeDir)

	result := e.Repair(context.Background())

	if !result.OK() || result.Action != ActionRepair {
		t.Fatalf("repair result = %+v", result)
	}
	if _, err := os.Lstat(pidfile); !os.IsNotExist(err) {
		t.Fatalf("pidfile should be removed, err=%v", err)
	}
	if !h.mgr.did("reset mysqld") || !h.mgr.did("start mysqld") || !strings.Contains(result.Message, pidfile) {
		t.Fatalf("repair must remove stale pidfile, reset failed state, then start, calls=%v message=%q", h.mgr.calls, result.Message)
	}
}

// A crashed unit whose daemon (or a child) survived must not have its failed
// marker cleared and a second instance started beside the survivor.
func TestRepairReconcilesSurvivorsBeforeResetAndStart(t *testing.T) {
	h := defaultHarness()
	h.mgr.status = servicemgr.StatusFailed
	h.discoverSteps = [][]process.Process{{{PID: 4242, Exe: "/usr/sbin/mysqld", ExeOK: true, UID: 110, StartTicks: 9}}}
	h.killPolicy = process.KillPolicy{ForceKill: false}
	e := h.engine()
	e.RepairStalePIDFiles = repairStalePIDFiles(h.mgr, e.Unit, nil, repairReader{}, t.TempDir())

	result := e.Repair(context.Background())

	if result.Status != ResultOrphanProcesses || !strings.Contains(result.Message, "before repair") {
		t.Fatalf("repair result = %s %q, want orphan_processes before repair", result.Status, result.Message)
	}
	if h.mgr.did("reset mysqld") || h.mgr.did("start mysqld") {
		t.Fatalf("survivor must block reset and start, calls=%v", h.mgr.calls)
	}
}

// resetStopped revalidates absence right before the reset: a process that
// appears after reconciliation still blocks the zap/reset-failed and the start.
func TestRepairRevalidatesAbsenceBeforeReset(t *testing.T) {
	h := defaultHarness()
	h.mgr.status = servicemgr.StatusFailed
	e := h.engine()
	e.RepairStalePIDFiles = repairStalePIDFiles(h.mgr, e.Unit, nil, repairReader{}, t.TempDir())
	e.ObserveProcesses = func() (process.Observation, error) {
		return process.Observation{Processes: []process.Process{{PID: 4242, Exe: "/usr/sbin/mysqld", ExeOK: true}}}, nil
	}

	result := e.Repair(context.Background())

	if result.OK() || !strings.Contains(result.Message, "processes appeared before init state reconciliation") {
		t.Fatalf("repair result = %s %q, want refusal to reset with a live process", result.Status, result.Message)
	}
	if h.mgr.did("reset mysqld") || h.mgr.did("start mysqld") {
		t.Fatalf("reset and start must not run, calls=%v", h.mgr.calls)
	}
}

func TestRepairDoesNotResetInactiveService(t *testing.T) {
	mgr := &fakeManager{status: servicemgr.StatusInactive}
	prepare := repairStalePIDFiles(mgr, "rabbitmq", nil, repairReader{}, t.TempDir())

	removed, err := prepare(context.Background())

	if err != nil || len(removed) != 0 {
		t.Fatalf("repair preparation = %v, %v", removed, err)
	}
	if mgr.did("reset rabbitmq") {
		t.Fatalf("inactive service must not reset backend state, calls=%v", mgr.calls)
	}
}

func TestRepairRefusesLivePIDFile(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pidfile := filepath.Join(runtimeDir, "rabbitmq.pid")
	if err := os.WriteFile(pidfile, []byte("5023\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := defaultHarness()
	h.mgr.status = servicemgr.StatusFailed
	e := h.engine()
	e.RepairStalePIDFiles = repairStalePIDFiles(h.mgr, e.Unit, []process.Selector{{
		Name: process.SelectorPidfile, Type: process.SelectorPidfile, Paths: []string{pidfile},
	}}, repairReader{5023: true}, runtimeDir)

	result := e.Repair(context.Background())

	if result.OK() || !strings.Contains(result.Message, "pid 5023 is running") {
		t.Fatalf("repair result = %+v, want live-pid refusal", result)
	}
	if _, err := os.Lstat(pidfile); err != nil || h.mgr.did("start mysqld") {
		t.Fatalf("live pidfile must remain and start must not run, err=%v calls=%v", err, h.mgr.calls)
	}
}

func TestRepairRefusesPIDFileOutsideRuntimeDirectory(t *testing.T) {
	root := t.TempDir()
	runtimeDir := filepath.Join(root, "run")
	if err := os.Mkdir(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pidfile := filepath.Join(root, "rabbitmq.pid")
	if err := os.WriteFile(pidfile, []byte("5023\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := &fakeManager{status: servicemgr.StatusFailed}
	prepare := repairStalePIDFiles(mgr, "rabbitmq", []process.Selector{{
		Name: process.SelectorPidfile, Type: process.SelectorPidfile, Paths: []string{pidfile},
	}}, repairReader{}, runtimeDir)

	_, err := prepare(context.Background())

	if err == nil || !strings.Contains(err.Error(), "outside runtime directory") {
		t.Fatalf("repair error = %v, want runtime-directory refusal", err)
	}
	if _, err := os.Lstat(pidfile); err != nil {
		t.Fatalf("outside pidfile must remain: %v", err)
	}
}

func TestRepairRequiresActiveThroughPostflight(t *testing.T) {
	for _, status := range []servicemgr.Status{servicemgr.StatusActive, servicemgr.StatusInactive, servicemgr.StatusUnknown, servicemgr.StatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			h := defaultHarness()
			h.mgr.stopped = true
			e := h.engine()
			e.Lifecycle.ProcessMode = config.ServiceProcessNone
			e.RepairStalePIDFiles = func(context.Context) ([]string, error) { return nil, nil }
			e.Postflight = func(context.Context) checks.Outcome {
				h.mgr.status = status
				return checks.Outcome{OK: true}
			}
			result := e.Repair(t.Context())
			if result.OK() != (status == servicemgr.StatusActive) {
				t.Fatalf("result=%+v, final backend=%s", result, status)
			}
			if !h.mgr.did("start mysqld") || len(h.emitted) != 1 || h.released != 1 {
				t.Fatalf("calls=%v events=%v released=%d", h.mgr.calls, h.emitted, h.released)
			}
		})
	}
}
