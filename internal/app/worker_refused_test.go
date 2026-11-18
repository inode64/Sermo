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

func TestWorkerPreflightRefusalSpacing(t *testing.T) {
	h := &workerHarness{cache: failedCache("http"), opResult: operation.Result{
		Status: operation.ResultPreflightFailed, Message: "preflight failed",
		Checks: []checks.Result{{Check: "libraries", Message: "missing library"}},
	}}
	w := h.worker(alertThenRestartTree("http"), rules.Policy{Cooldown: time.Minute}, nil)
	at := t0
	w.Now = func() time.Time { return at }
	for minute, want := range []int{1, 2, 2, 3, 4} {
		at = t0.Add(time.Duration(minute) * time.Minute)
		if minute == 0 {
			h.opResult.Checks[0].Message = "pid=100 missing library"
		}
		if minute == 1 {
			h.opResult.Checks[0].Message = "pid=160 missing library"
		}
		if minute == 3 {
			h.opResult.Checks[0].Message = "library unreadable"
		}
		w.RunCycle(t.Context())
		if len(h.ops) != want || h.countEvents(eventKindError) != want {
			t.Fatalf("minute %d: operations=%d audit events=%d, want %d", minute, len(h.ops), h.countEvents(eventKindError), want)
		}
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

// The audit details quote the host, so a placeholder in a check's output stays
// as the program printed it, as in the manual operation's event, while the
// message, which may be a guard's, still expands its runtime built-ins.
func TestWorkerRemediationEventKeepsHostTextLiteral(t *testing.T) {
	const output = "exit 1 (want 0): AH00111: Config variable ${event} is not defined at ${date}"
	refused := operation.Result{Status: operation.ResultBlocked, Message: "blocked for ${event}", Checks: []checks.Result{
		{Check: "config", Message: output},
	}}
	h := &workerHarness{cache: failedCache("http"), opResult: refused}
	w := h.worker(alertThenRestartTree("http"), rules.Policy{Cooldown: time.Minute}, nil)
	w.RunCycle(context.Background())
	event, ok := h.eventOf(eventKindSuppressed)
	if !ok || event.Message != "blocked for restart-on-stale-binary; check config failed: "+output || event.Service != "web" {
		t.Fatalf("event = %+v, want the message expanded and the host text as written", event)
	}
}

// A guard the engine re-evaluates at the operation can block an attempt the
// worker let through; its readings join the audit text as written too.
func TestWorkerEngineGuardBlockKeepsReadingsLiteral(t *testing.T) {
	refused := operation.Result{Status: operation.ResultBlocked, Message: "blocked for ${event}", Guard: "packages",
		GuardEvidence: "process: state running (want absent): pid 41 (ppid 1) /opt/${date}/emerge"}
	h := &workerHarness{cache: failedCache("http"), opResult: refused}
	w := h.worker(alertThenRestartTree("http"), rules.Policy{Cooldown: time.Minute}, nil)
	w.RunCycle(context.Background())
	event, ok := h.eventOf(eventKindSuppressed)
	if want := "blocked for restart-on-stale-binary; observed " + refused.GuardEvidence; !ok || event.Message != want {
		t.Fatalf("event = %+v, want message %q", event, want)
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

// The refusal event names the reading that held the guard, but the guard's
// message alone identifies the refusal: another PID behind the same guard is
// not a new reason to audit at once. The guard's message expands its runtime
// built-ins; the reading quotes the host and stays as written.
func TestWorkerGuardRefusalNamesEvidenceWithoutTreatingItAsNews(t *testing.T) {
	h := &workerHarness{cache: failedCache("http"), opResult: operation.Result{Status: operation.ResultOK}}
	h.cache["packages"] = checks.Result{OK: true, Message: "state running (want running): pid 41 (ppid 1) /opt/${date}/emerge"}
	w := h.worker(alertThenRestartTree("http"), rules.Policy{Cooldown: time.Minute}, nil)
	w.Rules = append(w.Rules, rules.Rule{Name: "packages", Type: rules.RuleGuard, Blocks: []string{"restart"},
		If:      map[string]any{"active": map[string]any{"check": "packages"}},
		Actions: []rules.Action{{Type: rules.ActionBlock, Message: "Package update is running on ${service}"}}})
	w.RunCycle(t.Context())
	event, ok := h.eventOf(eventKindSuppressed)
	if want := "guard: Package update is running on web; observed packages: state running (want running): pid 41 (ppid 1) /opt/${date}/emerge"; !ok || event.Message != want {
		t.Fatalf("event = %+v, want message %q", event, want)
	}
	h.cache["packages"] = checks.Result{OK: true, Message: "state running (want running): pid 42 (ppid 1) /opt/${date}/emerge"}
	w.RunCycle(t.Context())
	if got := h.countEvents(eventKindSuppressed); got != 1 || len(h.ops) != 0 {
		t.Fatalf("refusals audited=%d ops=%v, want one audit while the guard's reason is unchanged", got, h.ops)
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
		{30 * time.Minute, 6, maxRefusalInterval},
		{30 * time.Minute, 200, maxRefusalInterval},
		// A cooldown longer than the cap is never shortened.
		{48 * time.Hour, 3, 48 * time.Hour},
	} {
		if got := refusalInterval(tc.cooldown, tc.repeats); got != tc.want {
			t.Errorf("refusalInterval(%v, %d) = %v, want %v", tc.cooldown, tc.repeats, got, tc.want)
		}
	}
}

func restartIfServiceFailedTree() map[string]any {
	return map[string]any{"rules": map[string]any{"restart-if-service-failed": map[string]any{
		"type": "remediation",
		"if":   map[string]any{"failed": map[string]any{"check": "service"}},
		"then": map[string]any{"action": "restart"},
	}}}
}

// A restart the config preflight refused waits for the cooldown only while the
// configuration stays broken: once the configuration check, which runs the
// same command, passes again, the next cycle retries. A refusal by another
// preflight check keeps its cooldown, because a passing config proves nothing
// about it.
func TestWorkerConfigPreflightRefusalEndsWhenConfigurationPasses(t *testing.T) {
	for name, tc := range map[string]struct {
		failedCheck string
		wantRetry   bool
	}{
		"config preflight":    {failedCheck: "config", wantRetry: true},
		"libraries preflight": {failedCheck: "update-libraries", wantRetry: false},
	} {
		t.Run(name, func(t *testing.T) {
			broken := checks.Result{Check: "configuration", OK: false, Message: "exit 1 (want 0): AH00526: Syntax error"}
			h := &workerHarness{
				cache: map[string]checks.Result{"service": {Check: "service", OK: false}, "configuration": broken},
				opResult: operation.Result{Status: operation.ResultPreflightFailed, Message: "preflight failed",
					Checks: []checks.Result{{Check: tc.failedCheck, OK: false}}},
			}
			w := h.worker(restartIfServiceFailedTree(), rules.Policy{Cooldown: 30 * time.Minute}, nil)
			at := t0
			w.Now = func() time.Time { return at }

			w.RunCycle(context.Background())
			at = at.Add(30 * time.Second)
			w.RunCycle(context.Background())
			if len(h.ops) != 1 {
				t.Fatalf("ops=%v, want one refused attempt while the configuration is broken", h.ops)
			}

			at = at.Add(30 * time.Second)
			h.cache["configuration"] = checks.Result{Check: "configuration", OK: true, Message: "exit 0 (want 0)"}
			h.opResult = operation.Result{Status: operation.ResultOK}
			w.RunCycle(context.Background())
			if got := len(h.ops) == 2; got != tc.wantRetry {
				t.Fatalf("ops=%v, want retry=%v once the configuration passes", h.ops, tc.wantRetry)
			}
		})
	}
}

// While a config-preflight refusal waits, the configuration check reruns
// beyond its own interval at most once per configurationRecheckInterval, and
// not at all once nothing waits for it.
func TestConfigurationRecheckDuePacesTheRerun(t *testing.T) {
	built := []checks.Built{{Check: stubCheck{name: "service"}}, {Check: stubCheck{name: "configuration"}}}
	w := &Worker{cycleRan: ranChecks("service")}
	if got := w.configurationRecheckDue(built, t0); got != nil {
		t.Fatalf("no refusal waits, got %d checks", len(got))
	}
	w.refusedRemediations = map[string]remediationRefusal{"restart-if-service-failed": {configPreflight: true, retryAt: t0.Add(time.Hour)}}
	if got := w.configurationRecheckDue(built, t0); len(got) != 1 || got[0].Check.Name() != "configuration" {
		t.Fatalf("got %v, want the configuration check", got)
	}
	if got := w.configurationRecheckDue(built, t0.Add(configurationRecheckInterval-time.Second)); got != nil {
		t.Fatal("reran inside the recheck interval")
	}
	if got := w.configurationRecheckDue(built, t0.Add(configurationRecheckInterval)); len(got) != 1 {
		t.Fatal("did not rerun after the recheck interval")
	}
	w.cycleRan = ranChecks("service", "configuration")
	if got := w.configurationRecheckDue(built, t0.Add(2*configurationRecheckInterval)); got != nil {
		t.Fatal("reran a configuration check its own interval already ran this cycle")
	}
}
