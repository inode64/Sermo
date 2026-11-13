package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/emission"
	"sermo/internal/notify"
	"sermo/internal/operation"
	"sermo/internal/rules"
)

// Refusals wait before another attempt without consuming the executed-action
// budget. Every actual attempt remains audited, with one alert per episode.
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
			at := t0
			w.Now = func() time.Time { return at }

			const cycles = 3
			for range cycles {
				w.RunCycle(context.Background())
			}
			if len(h.ops) != 1 || h.countEvents(tc.kind) != 1 {
				t.Fatalf("ops=%v events=%+v, want one audited refusal during cooldown", h.ops, h.events)
			}
			if !w.State.LastActionAt.IsZero() || len(w.State.RecentActions) != 0 || w.State.CurrentBackoff != 0 {
				t.Fatalf("refusal consumed the executed-action budget: %+v", w.State)
			}
			if got := h.countEvents(eventKindAlert); got != 1 || len(ops.msgs) != 1 {
				t.Fatalf("alerts=%d notifications=%d, want one of each for the episode; events=%+v", got, len(ops.msgs), h.events)
			}

			// The attempt that gets through is the action the alert announced.
			at = at.Add(time.Minute)
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

func TestWorkerGuardRefusalCooldownKeepsFreshEvidence(t *testing.T) {
	for _, mode := range []string{emission.ModeOnChange, emission.ModeEveryCycle} {
		t.Run(mode, func(t *testing.T) {
			h := &workerHarness{cache: failedCache("http"), opResult: operation.Result{Status: operation.ResultOK}}
			h.cache["backup"] = checks.Result{OK: false}
			w := h.worker(alertThenRestartTree("http"), rules.Policy{Cooldown: time.Minute}, nil)
			w.GlobalEmission = emission.Policy{Events: mode}
			w.Rules = append(w.Rules, rules.Rule{Name: "backup", Type: rules.RuleGuard, Blocks: []string{"restart"},
				If:      map[string]any{"failed": map[string]any{"check": "backup"}},
				Actions: []rules.Action{{Type: rules.ActionBlock, Message: "backup running"}}})
			at := t0
			w.Now = func() time.Time { return at }
			for range 3 {
				w.RunCycle(t.Context())
			}
			if len(h.ops) != 0 || h.countEvents(eventKindSuppressed) != 1 {
				t.Fatalf("unchanged guard: ops=%v events=%+v", h.ops, h.events)
			}
			at = at.Add(time.Minute)
			w.RunCycle(t.Context())
			if h.countEvents(eventKindSuppressed) != 2 {
				t.Fatalf("retry not audited: %+v", h.events)
			}
			w.Rules[len(w.Rules)-1].Actions[0].Message = "different backup"
			w.RunCycle(t.Context())
			if h.countEvents(eventKindSuppressed) != 3 {
				t.Fatalf("changed reason not audited: %+v", h.events)
			}
			h.cache["backup"] = checks.Result{OK: true}
			w.RunCycle(t.Context())
			if len(h.ops) != 1 || h.countEvents(eventKindAlert) != 1 {
				t.Fatalf("cleared guard must release and announce the first attempt: ops=%v events=%+v", h.ops, h.events)
			}
		})
	}
}

func TestWorkerRefusalEndsWithItsEpisode(t *testing.T) {
	h := &workerHarness{cache: failedCache("http"), opResult: operation.Result{Status: operation.ResultPreflightFailed}}
	w := h.worker(remediationTree("restart-if-down", "http", "restart"), rules.Policy{Cooldown: time.Hour}, nil)
	w.RunCycle(t.Context())
	h.cache["http"] = checks.Result{OK: true}
	w.RunCycle(t.Context())
	h.cache = failedCache("http")
	w.RunCycle(t.Context())
	if len(h.ops) != 2 {
		t.Fatalf("previous incident delayed a new incident: %v", h.ops)
	}
}
