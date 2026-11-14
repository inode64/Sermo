package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/notify"
	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/severity"
)

// alertThenRestartTree is the shape the generated stale-binary rule has: an
// alert followed by the restart, on one check.
func alertThenRestartTree(check string) map[string]any {
	return map[string]any{"rules": map[string]any{"restart-on-stale-binary": map[string]any{
		"type": "remediation",
		"if":   map[string]any{"failed": map[string]any{"check": check}},
		"then": map[string]any{"actions": []any{
			map[string]any{"type": "alert", "message": "binary replaced"},
			map[string]any{"type": "restart"},
		}},
	}}}
}

// Advisory evidence (a binary removed from disk, or replaced only beside the
// daemon) keeps the alert of a rule that alerts and restarts, but never runs the
// restart: the notifier hears the alert with the reading that held the action
// back, the event log records the withheld action, and the recovery reaches the
// same notifier once the reading clears.
func TestWorkerAdvisoryEvidenceAlertsWithoutOperating(t *testing.T) {
	advisory := checks.Result{Check: "stale", OK: false, ActionBlocked: true, Message: "1 process(es) beside the daemon run a replaced binary (/usr/bin/bash)"}
	h := &workerHarness{cache: map[string]checks.Result{"stale": advisory}, opResult: operation.Result{Status: operation.ResultOK}}
	w := h.worker(alertThenRestartTree("stale"), rules.Policy{Cooldown: time.Minute}, nil)
	ops := &fakeNotifier{name: "ops", typ: "email"}
	w.Notifiers = map[string]notify.Notifier{"ops": ops}
	w.GlobalNotify = []string{"ops"}

	w.RunCycle(context.Background())
	w.RunCycle(context.Background())

	if len(h.ops) != 0 {
		t.Fatalf("advisory evidence ran %v", h.ops)
	}
	if got := h.countEvents(eventKindAlert); got != 1 {
		t.Fatalf("alert events = %d, want one per episode; events=%+v", got, h.events)
	}
	alert, _ := h.eventOf(eventKindAlert)
	if !strings.Contains(alert.Output, "check stale: "+advisory.Message) {
		t.Fatalf("alert output %q must say why nothing ran", alert.Output)
	}
	suppressed, ok := h.eventOf(eventKindSuppressed)
	if !ok || suppressed.Action != string(rules.ActionRestart) || !strings.HasPrefix(suppressed.Message, advisoryRemediationPrefix) {
		t.Fatalf("withheld action not audited: %+v", h.events)
	}
	if h.countEvents(eventKindSuppressed) != 1 {
		t.Fatalf("the withheld action is recorded once per episode: %+v", h.events)
	}
	if len(ops.msgs) != 1 || !strings.Contains(ops.msgs[0].Body, advisory.Message) {
		t.Fatalf("notifier got %+v, want the alert with its advisory reading", ops.msgs)
	}

	h.cache = map[string]checks.Result{"stale": {Check: "stale", OK: true}}
	w.RunCycle(context.Background())
	if _, ok := h.eventOf(eventKindRecovered); !ok || len(ops.msgs) != 2 {
		t.Fatalf("recovery lost: events=%+v notifications=%d", h.events, len(ops.msgs))
	}
}

// Once the same reading becomes actionable (the package is installed again),
// the open episode acts on its next cycle.
func TestWorkerAdvisoryEpisodeActsOnceEvidenceIsActionable(t *testing.T) {
	h := &workerHarness{cache: map[string]checks.Result{"stale": {Check: "stale", OK: false, ActionBlocked: true}}, opResult: operation.Result{Status: operation.ResultOK}}
	w := h.worker(alertThenRestartTree("stale"), rules.Policy{Cooldown: time.Minute}, nil)
	w.RunCycle(context.Background())
	h.cache = map[string]checks.Result{"stale": {Check: "stale", OK: false}}
	w.RunCycle(context.Background())
	if len(h.ops) != 1 || h.ops[0] != string(rules.ActionRestart) {
		t.Fatalf("ops = %v, want the restart once the evidence is actionable", h.ops)
	}
}

// A rule with no alert has nothing to keep, and a busy reading stays unknown
// for an operation rule: neither alerts, records a withheld action nor acts.
func TestWorkerAdvisoryNeedsAnAlertAndIgnoresBusy(t *testing.T) {
	for name, tc := range map[string]struct {
		tree map[string]any
		res  checks.Result
	}{
		"restart only": {remediationTree("restart-if-stale", "stale", "restart"), checks.Result{Check: "stale", OK: false, ActionBlocked: true}},
		"busy":         {alertThenRestartTree("stale"), checks.Result{Check: "stale", OK: false, Busy: true}},
	} {
		t.Run(name, func(t *testing.T) {
			h := &workerHarness{cache: map[string]checks.Result{"stale": tc.res}, opResult: operation.Result{Status: operation.ResultOK}}
			w := h.worker(tc.tree, rules.Policy{Cooldown: time.Minute}, nil)
			w.RunCycle(context.Background())
			if len(h.ops) != 0 || h.countEvents(eventKindAlert) != 0 || h.countEvents(eventKindSuppressed) != 0 {
				t.Fatalf("ops=%v events=%+v, want no action, alert or withheld action", h.ops, h.events)
			}
		})
	}
}

// A restart-only rule whose check cannot load its own program's library never
// restarts, alerts nor notifies, but records the withheld restart once per
// episode with the reading, so the event log says why remediation is idle; the
// episode closes once the library is installed.
func TestWorkerRestartOnlyRuleRecordsAProgramMissingALibrary(t *testing.T) {
	const missing = "ModuleNotFoundError: No module named 'prettytable'"
	broken := checks.Result{
		Check: "mon-status", OK: false, ActionBlocked: true, Severity: severity.Warning,
		Message: "exit 1; program could not load a library: " + missing,
		Data:    map[string]any{checks.DataKeyMissingLibrary: missing},
	}
	h := &workerHarness{cache: map[string]checks.Result{"mon-status": broken}, opResult: operation.Result{Status: operation.ResultOK}}
	w := h.worker(remediationTree("restart-if-status-failed", "mon-status", "restart"), rules.Policy{Cooldown: time.Minute}, nil)
	ops := &fakeNotifier{name: "ops", typ: "email"}
	w.Notifiers = map[string]notify.Notifier{"ops": ops}
	w.GlobalNotify = []string{"ops"}

	w.RunCycle(context.Background())
	w.RunCycle(context.Background())

	if len(h.ops) != 0 || h.countEvents(eventKindAlert) != 0 || len(ops.msgs) != 0 {
		t.Fatalf("ops=%v events=%+v notifications=%d, want no action, alert or notification", h.ops, h.events, len(ops.msgs))
	}
	suppressed, ok := h.eventOf(eventKindSuppressed)
	if !ok || h.countEvents(eventKindSuppressed) != 1 || suppressed.Action != string(rules.ActionRestart) ||
		suppressed.Severity != severity.Warning || !strings.Contains(suppressed.Message, missing) {
		t.Fatalf("withheld restart not recorded once as a warning with its reading: %+v", h.events)
	}

	h.cache = map[string]checks.Result{"mon-status": {Check: "mon-status", OK: true}}
	w.RunCycle(context.Background())
	if _, ok := h.eventOf(eventKindRecovered); !ok || len(h.ops) != 0 {
		t.Fatalf("installing the library must close the episode without acting: ops=%v events=%+v", h.ops, h.events)
	}
}

// A reload asks the running process to read the new configuration and load
// its modules. After the daemon's own binary was replaced, that is the old
// executable loading the new version's modules, which killed Apache; the
// reload waits for the restart the stale-binary reading drives. Helpers beside
// the daemon, or no stale binary at all, leave the reload alone.
func TestWorkerHoldsAReloadWhileTheDaemonRunsAReplacedBinary(t *testing.T) {
	tree := map[string]any{"rules": map[string]any{"reload-on-change-1": map[string]any{
		"type": "remediation",
		"if":   map[string]any{"failed": map[string]any{"check": "config-changed"}},
		"then": map[string]any{"action": "reload"},
	}}}
	const replaced = "1 process(es) run a replaced binary (/usr/sbin/apache2); restart to pick up the installed version"
	for name, tc := range map[string]struct {
		stale  checks.Result
		reload bool
	}{
		"daemon replaced":  {stale: checks.Result{Check: "stale-binary", Message: replaced}},
		"helpers replaced": {stale: checks.Result{Check: "stale-binary", ActionBlocked: true, Message: "1 process(es) beside the daemon run a replaced binary (/usr/bin/su)"}, reload: true},
		"nothing replaced": {stale: checks.Result{Check: "stale-binary", OK: true}, reload: true},
	} {
		t.Run(name, func(t *testing.T) {
			cache := failedCache("config-changed")
			cache["stale-binary"] = tc.stale
			h := &workerHarness{cache: cache, opResult: operation.Result{Status: operation.ResultOK}}
			w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, nil)
			w.RunCycle(context.Background())
			if tc.reload {
				if len(h.ops) != 1 || h.ops[0] != "reload" {
					t.Fatalf("ops=%v events=%+v, want the reload", h.ops, h.events)
				}
				return
			}
			event, ok := h.eventOf(eventKindSuppressed)
			if len(h.ops) != 0 || !ok || !strings.Contains(event.Message, "reload withheld: "+replaced) {
				t.Fatalf("ops=%v events=%+v, want the reload held with the reading", h.ops, h.events)
			}
		})
	}
}
