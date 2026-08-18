package operation

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sermo/internal/process"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
)

type repairReader map[int]bool

func (r repairReader) PIDs() ([]int, error) { return nil, nil }

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
	h.mgr.statusSteps = []servicemgr.Status{servicemgr.StatusFailed}
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
