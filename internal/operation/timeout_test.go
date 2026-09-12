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

// A config reload (SIGHUP) or shutdown cancels the operation context, and every
// `--with-config` deployment reloads the daemon. Reporting that as a timeout sent
// the operator looking for a slow service that did not exist: observed on k2kca2,
// where a reload landed 2s after a successful automatic restart and the event log
// read "operation timed out during postflight" for a service that was up.
func TestCancelledOperationIsNotReportedAsATimeout(t *testing.T) {
	for _, tc := range []struct {
		name     string
		phase    func(*harness)
		wantWait string
	}{
		{
			name:     "graceful stop wait",
			phase:    func(h *harness) { h.killPolicy = process.KillPolicy{GracefulTimeout: time.Hour} },
			wantWait: "graceful stop wait",
		},
		{
			// Ready on the first attempt: the operation only owes the bounded
			// settling sleeps, which is exactly where the reload landed.
			name:     "postflight",
			phase:    func(*harness) {},
			wantWait: "postflight",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := defaultHarness()
			tc.phase(h)
			eng := h.engine()
			ctx, cancel := context.WithCancel(context.Background())
			eng.Sleep = func(time.Duration) { cancel() }

			res := eng.Restart(ctx)

			if res.Status != ResultFailed {
				t.Fatalf("status = %q, want failed", res.Status)
			}
			if want := "operation cancelled during " + tc.wantWait; res.Message != want {
				t.Fatalf("message = %q, want %q", res.Message, want)
			}
		})
	}
}

func timeoutFromTree(configured time.Duration, tree map[string]any) time.Duration {
	policy, _ := process.ParseStopPolicy(tree)
	selectors, _ := process.ParseSelectors(tree)
	return resolveTimeout(configured, process.EnableAutomaticReaping(policy, selectors))
}

func TestResolvedTimeoutHonorsStopPolicy(t *testing.T) {
	tree := map[string]any{"stop_policy": map[string]any{"graceful_timeout": "120s"}}
	got := timeoutFromTree(90*time.Second, tree)
	want := 120*time.Second + backendMargin
	if got != want {
		t.Fatalf("resolved timeout = %v, want %v", got, want)
	}
}

func TestResolvedTimeoutKeepsLargerConfigured(t *testing.T) {
	tree := map[string]any{"stop_policy": map[string]any{"graceful_timeout": "120s"}}
	got := timeoutFromTree(5*time.Minute, tree)
	if got != 5*time.Minute {
		t.Fatalf("configured override = %v, want 5m", got)
	}
}

func TestResolvedTimeoutForceKillEscalation(t *testing.T) {
	tree := map[string]any{"stop_policy": map[string]any{
		"graceful_timeout": "10s",
		"term_timeout":     "20s",
		"kill_timeout":     "5s",
		"force_kill":       true,
		"kill_only_if":     map[string]any{"users": []any{"mysql"}},
	}}
	got := timeoutFromTree(30*time.Second, tree)
	want := 10*time.Second + 20*time.Second + 5*time.Second + backendMargin
	if got != want {
		t.Fatalf("resolved timeout = %v, want %v", got, want)
	}
}

func TestResolvedTimeoutAutomaticEscalation(t *testing.T) {
	tree := map[string]any{"stop_policy": map[string]any{
		"graceful_timeout": "10s",
		"term_timeout":     "20s",
		"kill_timeout":     "5s",
		"force_kill":       process.StopPolicyForceKillAuto,
	}, "processes": map[string]any{
		"main": map[string]any{"exe": "/usr/sbin/svc", "user": "svc"},
	}}
	got := timeoutFromTree(30*time.Second, tree)
	want := 10*time.Second + 20*time.Second + 5*time.Second + backendMargin
	if got != want {
		t.Fatalf("resolved timeout = %v, want %v", got, want)
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
