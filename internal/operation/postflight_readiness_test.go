package operation

import (
	"context"
	"testing"

	"sermo/internal/checks"
	"sermo/internal/servicemgr"
)

func TestPostflightOnlySamplesReadyService(t *testing.T) {
	for _, action := range []string{actionStart, actionRestart, actionResume} {
		for _, tc := range []struct {
			name       string
			states     []servicemgr.Status
			wantChecks int
			wantOK     bool
		}{
			{"settles then probe fails", []servicemgr.Status{servicemgr.StatusInactive, servicemgr.StatusInactive, servicemgr.StatusInactive, servicemgr.StatusInactive, servicemgr.StatusActive}, 1, false},
			{"settles then probe passes", []servicemgr.Status{servicemgr.StatusInactive, servicemgr.StatusInactive, servicemgr.StatusInactive, servicemgr.StatusInactive, servicemgr.StatusActive}, 1, true},
			{"loses readiness", []servicemgr.Status{servicemgr.StatusActive, servicemgr.StatusInactive, servicemgr.StatusInactive, servicemgr.StatusInactive, servicemgr.StatusActive}, 2, false},
			{"never ready", []servicemgr.Status{servicemgr.StatusInactive, servicemgr.StatusInactive, servicemgr.StatusInactive, servicemgr.StatusInactive, servicemgr.StatusInactive}, 0, false},
		} {
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				h := defaultHarness()
				h.mgr.statusSteps = tc.states
				e := h.engine()
				calls := 0
				e.Postflight = func(context.Context) checks.Outcome {
					calls++
					// A premature probe would pass and hide the final failed
					// observation; only a sample after the last transition counts.
					ok := h.mgr.statusCalls < len(tc.states) || tc.wantOK
					return checks.Outcome{OK: ok, Results: []checks.Result{{Check: "ready", OK: ok}}}
				}
				result := Result{Action: action, Status: ResultOK}
				p := plan{postflight: true, start: action != actionResume, resume: action == actionResume}
				got := e.runPostflight(t.Context(), p, &result)
				if got != tc.wantOK || calls != tc.wantChecks {
					t.Fatalf("ready=%v checks=%d result=%+v", got, calls, result)
				}
				if !tc.wantOK && result.OK() {
					t.Fatalf("failed readiness reported success: %+v", result)
				}
			})
		}
	}
}
