package app

import (
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/notify"
	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/severity"
)

func TestMaintenanceAttachedAlertsWaitForOutcome(t *testing.T) {
	for _, tc := range []struct {
		name      string
		checkType string
		status    operation.ResultStatus
		notice    bool
		alertOnly bool
	}{
		{"config reload", "", operation.ResultOK, true, false},
		{"renewed libraries", checks.CheckTypeStaleLibraries, operation.ResultOK, true, false},
		{"renewed binary", checks.CheckTypeStaleBinary, operation.ResultOK, true, false},
		{"failed renewal", checks.CheckTypeStaleLibraries, operation.ResultFailed, false, false},
		{"blocked renewal", checks.CheckTypeStaleLibraries, operation.ResultBlocked, false, false},
		{"health repair", checks.CheckTypeService, operation.ResultOK, false, false},
		{"manual renewal", checks.CheckTypeStaleLibraries, operation.ResultOK, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &workerHarness{opResult: operation.Result{Status: tc.status}}
			w := h.worker(nil, rules.Policy{Cooldown: time.Minute}, nil)
			w.MetricChecks = map[string]any{"trigger": map[string]any{"type": tc.checkType}}
			direct := &floorNotifier{name: "direct", floor: severity.Error}
			journal := &floorNotifier{name: "journal", floor: severity.Info}
			w.Notifiers = map[string]notify.Notifier{"direct": direct, "journal": journal}
			w.GlobalNotify = []string{"direct", "journal"}
			firing := firingRule{
				Name: "reconcile", Type: rules.RuleRemediation,
				If:      map[string]any{"failed": map[string]any{"check": "trigger"}},
				Actions: []rules.Action{{Type: rules.ActionAlert, Message: "renew service"}, {Type: rules.ActionReload}},
				holds:   true, rising: true, severity: severity.Error,
			}
			if tc.checkType == "" {
				firing.If = map[string]any{"changed": map[string]any{"file": "/etc/example.conf"}}
			}
			if tc.alertOnly {
				firing.Type = rules.RuleAlert
				firing.Actions = firing.Actions[:1]
			}
			ev := &rules.Evaluator{Cache: failedCache("trigger")}
			w.windowState(firing.Name).FiresGradedAt(firing.Rule, true, severity.Error, t0)
			w.runFiringRemediation(t.Context(), ev, w.Now, firing)
			w.emitRuleRecovered(t.Context(), ev, firing.Rule, ruleFiringState{
				severity: severity.Error, notified: w.windowState(firing.Name).Notified(),
			})
			all := &floorNotifier{name: "all", floor: severity.Info}
			oncall := &floorNotifier{name: "oncall", floor: severity.Error}
			router := newTestEventNotifier(t, all, oncall)
			for _, event := range h.events {
				if eventNeedsNotification(event) {
					router.deliver(t.Context(), eventNotification{Event: event})
				}
			}
			if tc.notice {
				if len(direct.msgs) != 0 || len(oncall.msgs) != 0 || len(all.msgs) != 2 || len(journal.msgs) != 1 {
					t.Fatalf("successful maintenance delivered direct=%d oncall=%d all=%d; want 0/0/2 notices", len(direct.msgs), len(oncall.msgs), len(all.msgs))
				}
			} else if len(direct.msgs) != 2 || len(oncall.msgs) == 0 {
				t.Fatalf("incident lost its alert/recovery: direct=%d oncall=%d", len(direct.msgs), len(oncall.msgs))
			}
		})
	}
}

func TestMaintenanceOutcomeNotifications(t *testing.T) {
	failed := map[string]any{"failed": map[string]any{"check": "trigger"}}
	for _, tc := range []struct {
		name      string
		condition map[string]any
		checkType string
		status    operation.ResultStatus
		notice    bool
	}{
		{"config reload", map[string]any{"changed": map[string]any{"file": "/etc/example.conf"}}, "", operation.ResultOK, true},
		{"library renewal", failed, checks.CheckTypeStaleLibraries, operation.ResultOK, true},
		{"binary renewal", failed, checks.CheckTypeStaleBinary, operation.ResultOK, true},
		{"failed renewal", failed, checks.CheckTypeStaleLibraries, operation.ResultFailed, false},
		{"blocked renewal", failed, checks.CheckTypeStaleLibraries, operation.ResultBlocked, false},
		{"health repair", failed, checks.CheckTypeService, operation.ResultOK, false},
		{"mixed condition", map[string]any{"and": []any{failed, map[string]any{"service": map[string]any{"state": "failed"}}}}, checks.CheckTypeStaleLibraries, operation.ResultOK, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &workerHarness{opResult: operation.Result{Status: tc.status}}
			w := h.worker(nil, rules.Policy{Cooldown: time.Minute}, nil)
			w.MetricChecks = map[string]any{"trigger": map[string]any{"type": tc.checkType}}
			firing := firingRule{Name: "reconcile", If: tc.condition, severity: severity.Error}
			w.executeRemediation(t.Context(), w.Now, firing, "reload", nil)
			if len(h.events) != 1 {
				t.Fatalf("events = %+v, want one operation outcome", h.events)
			}
			e := h.events[0]
			if e.Notice != tc.notice || (e.Severity == severity.Info) != tc.notice {
				t.Fatalf("outcome = %+v, want informational notice %t", e, tc.notice)
			}
			all := &floorNotifier{name: "all", floor: severity.Info}
			oncall := &floorNotifier{name: "oncall", floor: severity.Error}
			router := newTestEventNotifier(t, all, oncall)
			router.deliver(t.Context(), eventNotification{Event: e})
			router.deliver(t.Context(), eventNotification{Service: w.Service, Rule: firing.Name, Kind: eventKindRecovered})
			if tc.notice {
				if len(all.msgs) != 1 || len(oncall.msgs) != 0 {
					t.Fatalf("maintenance sent %d informational and %d on-call messages", len(all.msgs), len(oncall.msgs))
				}
			} else if len(oncall.msgs) == 0 {
				t.Fatal("real failure or repair was hidden from on-call")
			}
		})
	}
}
