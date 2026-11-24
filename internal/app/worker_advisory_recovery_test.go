package app

import (
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/operation"
	"sermo/internal/rules"
)

func TestRestoredRemediationDoesNotRecoverWhileEvidenceIsAdvisory(t *testing.T) {
	const ruleName = "restart-if-stale"
	for _, tc := range []struct {
		name   string
		clears bool
	}{{"becomes actionable", false}, {"actually clears", true}} {
		t.Run(tc.name, func(t *testing.T) {
			tree := remediationTree(ruleName, "libraries", "restart")
			h := &workerHarness{cache: failedCache("libraries"), opResult: operation.Result{Status: operation.ResultBlocked}}
			w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, nil)
			w.RunCycle(t.Context())
			snapshot := w.windowState(ruleName).Snapshot()
			if !snapshot.Firing {
				t.Fatal("fixture did not open a remediation episode")
			}
			w = h.worker(tree, rules.Policy{Cooldown: time.Minute}, nil)
			w.windows = map[string]*rules.WindowState{ruleName: rules.WindowStateFromSnapshot(snapshot)}
			w.Now = func() time.Time { return t0.Add(10 * time.Minute) }
			h.events = nil
			h.cache = map[string]checks.Result{"libraries": {
				Check: "libraries", OK: false, ActionBlocked: true,
				Message: "1 stale library mapping; waiting for unchanged library identities",
			}}
			w.RunCycle(t.Context())
			w.RunCycle(t.Context())
			if len(h.events) != 0 || len(h.ops) != 1 || !w.windowState(ruleName).Firing() {
				t.Fatalf("advisory evidence recovered or operated: events=%+v ops=%v", h.events, h.ops)
			}
			h.cache = map[string]checks.Result{"libraries": {Check: "libraries", OK: tc.clears}}
			h.opResult.Status = operation.ResultOK
			w.RunCycle(t.Context())
			if tc.clears {
				if h.countEvents(eventKindRecovered) != 1 || len(h.ops) != 1 {
					t.Fatalf("real recovery lost or acted: events=%+v ops=%v", h.events, h.ops)
				}
			} else if h.countEvents(eventKindRecovered) != 0 || len(h.ops) != 2 {
				t.Fatalf("fresh actionable evidence did not resume the episode: events=%+v ops=%v", h.events, h.ops)
			}
		})
	}
}
