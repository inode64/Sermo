package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/notify"
	"sermo/internal/operation"
	"sermo/internal/rules"
)

// An attempt the engine refuses (an identity it cannot verify, a lock another
// operation holds, a failed preflight) starts no cooldown, so the rule tries
// again on every cycle. Each attempt keeps its audit event, but the alert and
// its notification belong to the episode, not to the attempt.
func TestWorkerRefusedRemediationAlertsOncePerEpisode(t *testing.T) {
	for name, tc := range map[string]struct {
		status operation.ResultStatus
		kind   string
	}{
		"blocked":          {operation.ResultBlocked, eventKindSuppressed},
		"skipped":          {operation.ResultSkipped, eventKindSuppressed},
		"preflight failed": {operation.ResultPreflightFailed, eventKindError},
	} {
		t.Run(name, func(t *testing.T) {
			h := &workerHarness{cache: failedCache("stale"), opResult: operation.Result{Status: tc.status, Message: "refused"}}
			w := h.worker(alertThenRestartTree("stale"), rules.Policy{Cooldown: time.Minute}, nil)
			ops := &fakeNotifier{name: "ops", typ: "email"}
			w.Notifiers = map[string]notify.Notifier{"ops": ops}
			w.GlobalNotify = []string{"ops"}

			const cycles = 3
			for range cycles {
				w.RunCycle(context.Background())
			}
			if len(h.ops) != cycles || h.countEvents(tc.kind) != cycles {
				t.Fatalf("ops=%v events=%+v, want %d attempts with one audit event each", h.ops, h.events, cycles)
			}
			if got := h.countEvents(eventKindAlert); got != 1 || len(ops.msgs) != 1 {
				t.Fatalf("alerts=%d notifications=%d, want one of each for the episode; events=%+v", got, len(ops.msgs), h.events)
			}

			// The attempt that gets through is the action the alert announced.
			h.opResult = operation.Result{Status: operation.ResultOK}
			w.RunCycle(context.Background())
			if h.countEvents(eventKindAction) != 1 || h.countEvents(eventKindAlert) != 1 {
				t.Fatalf("events=%+v, want the action under the episode's one alert", h.events)
			}
		})
	}
}

// An attempt that ran starts the cooldown, which is what spaces the next one:
// each of those is announced, as before.
func TestWorkerExecutedRemediationAnnouncesEachAttempt(t *testing.T) {
	h := &workerHarness{cache: failedCache("stale"), opResult: operation.Result{Status: operation.ResultFailed, Message: "restart failed"}}
	w := h.worker(alertThenRestartTree("stale"), rules.Policy{}, nil)
	w.RunCycle(context.Background())
	w.RunCycle(context.Background())
	if len(h.ops) != 2 || h.countEvents(eventKindAlert) != 2 {
		t.Fatalf("ops=%v events=%+v, want both attempts announced", h.ops, h.events)
	}
}
