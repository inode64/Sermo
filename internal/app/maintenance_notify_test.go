package app

import (
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/severity"
)

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
