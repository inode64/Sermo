package operation

import (
	"context"
	"testing"
	"time"

	"sermo/internal/process"
)

func TestRestartTimesOutDuringGracefulWait(t *testing.T) {
	h := defaultHarness()
	h.killPolicy = process.KillPolicy{GracefulTimeout: time.Hour}
	eng := h.engine()
	eng.Sleep = time.Sleep
	eng.OperationTimeout = 30 * time.Millisecond
	res := eng.Restart(context.Background())
	if res.Status != ResultFailed {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if res.Message != "operation timed out during graceful stop wait" {
		t.Fatalf("message = %q", res.Message)
	}
	if !h.mgr.did("stop mysqld") {
		t.Fatal("stop should have been attempted before timeout")
	}
	if h.mgr.did("start mysqld") {
		t.Fatal("must not start after timed-out stop phase")
	}
}

func TestBoundContextRespectsShorterParent(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	ctx, childCancel := boundContext(parent, time.Hour)
	defer childCancel()
	time.Sleep(20 * time.Millisecond)
	if ctx.Err() == nil {
		t.Fatal("child context should inherit parent deadline")
	}
}

func TestStopTimesOutDuringReaperWait(t *testing.T) {
	h := defaultHarness()
	h.discoverSteps = [][]process.Process{{{PID: 100, UID: 110, Exe: "/opt/x", ExeOK: true}}, {{PID: 100, UID: 110, Exe: "/opt/x", ExeOK: true}}}
	h.killPolicy = process.KillPolicy{
		ForceKill:   true,
		KillOnlyIf:  process.KillSelector{Users: []string{"mysql"}, ExeAny: []string{"/opt/x"}},
		TermTimeout: time.Hour,
	}
	h.reaper = process.Reaper{
		Signaler:    noopSignaler{},
		ResolveUser: func(string) (uint32, bool) { return 110, true },
		Sleep:       time.Sleep,
	}
	eng := h.engine()
	eng.Sleep = time.Sleep
	eng.OperationTimeout = 30 * time.Millisecond
	res := eng.Stop(context.Background())
	if res.Status != ResultFailed {
		t.Fatalf("status = %q, want failed (%s)", res.Status, res.Message)
	}
	if res.Message != "operation timed out during residual process handling" {
		t.Fatalf("message = %q", res.Message)
	}
}