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

// An automatic remediation's event carries the operation's audit text, so a
// preflight that refused it names the check and its reason, as a manual
// operation's event does.
func TestWorkerRemediationEventNamesTheFailedPreflightCheck(t *testing.T) {
	refused := operation.Result{Status: operation.ResultPreflightFailed, Message: "preflight failed", Checks: []checks.Result{
		{Check: "config", Message: "exit 1 (want 0): syntax error on line 3"},
	}}
	h := &workerHarness{cache: failedCache("http"), opResult: refused}
	w := h.worker(alertThenRestartTree("http"), rules.Policy{Cooldown: time.Minute}, nil)
	w.RunCycle(context.Background())
	event, ok := h.eventOf(eventKindError)
	if !ok || event.Message != "preflight failed; check config failed: exit 1 (want 0): syntax error on line 3" {
		t.Fatalf("events=%+v, want the refusal to name the failed preflight check", h.events)
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

// A guard that keeps refusing for the same reason (logged-in users, host
// infrastructure) is audited at doubling intervals instead of every cooldown;
// it is still read every cycle, so a new reason is audited at once and starts
// the spacing again.
func TestWorkerUnchangedGuardRefusalIsAuditedLessOften(t *testing.T) {
	h := &workerHarness{cache: failedCache("http"), opResult: operation.Result{Status: operation.ResultOK}}
	h.cache["sessions"] = checks.Result{OK: false}
	w := h.worker(alertThenRestartTree("http"), rules.Policy{Cooldown: 30 * time.Minute}, nil)
	w.Rules = append(w.Rules, rules.Rule{Name: "sessions", Type: rules.RuleGuard, Blocks: []string{"restart"},
		If:      map[string]any{"failed": map[string]any{"check": "sessions"}},
		Actions: []rules.Action{{Type: rules.ActionBlock, Message: "users are logged in"}}})
	at := t0
	w.Now = func() time.Time { return at }
	audited := func() int { return h.countEvents(eventKindSuppressed) }

	// Audits fall at 0, 30m, 1h30, 3h30 and 7h30: each wait twice the last.
	var auditTimes []time.Duration
	for elapsed := time.Duration(0); elapsed <= 8*time.Hour; elapsed += 5 * time.Minute {
		at = t0.Add(elapsed)
		before := audited()
		w.RunCycle(t.Context())
		if audited() > before {
			auditTimes = append(auditTimes, elapsed)
		}
	}
	want := []time.Duration{0, 30 * time.Minute, 90 * time.Minute, 210 * time.Minute, 450 * time.Minute}
	if len(auditTimes) != len(want) {
		t.Fatalf("audits at %v, want %v", auditTimes, want)
	}
	for i := range want {
		if auditTimes[i] != want[i] {
			t.Fatalf("audits at %v, want %v", auditTimes, want)
		}
	}
	if len(h.ops) != 0 {
		t.Fatalf("a refusing guard let an operation through: %v", h.ops)
	}

	// A new reason is news: audited at once, then spaced from the cooldown again.
	w.Rules[len(w.Rules)-1].Actions[0].Message = "a session is being closed"
	before := audited()
	w.RunCycle(t.Context())
	at = at.Add(30 * time.Minute)
	w.RunCycle(t.Context())
	if audited() != before+2 {
		t.Fatalf("changed reason: %d audits, want it at once and again after the cooldown", audited()-before)
	}
}

func TestGuardRefusalInterval(t *testing.T) {
	for _, tc := range []struct {
		cooldown time.Duration
		repeats  int
		want     time.Duration
	}{
		{30 * time.Minute, 0, 30 * time.Minute},
		{30 * time.Minute, 1, time.Hour},
		{30 * time.Minute, 5, 16 * time.Hour},
		{30 * time.Minute, 6, maxGuardRefusalInterval},
		{30 * time.Minute, 200, maxGuardRefusalInterval},
		// A cooldown longer than the cap is never shortened.
		{48 * time.Hour, 3, 48 * time.Hour},
	} {
		if got := guardRefusalInterval(tc.cooldown, tc.repeats); got != tc.want {
			t.Errorf("guardRefusalInterval(%v, %d) = %v, want %v", tc.cooldown, tc.repeats, got, tc.want)
		}
	}
}
