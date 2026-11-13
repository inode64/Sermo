package rules

import (
	"context"
	"regexp"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/process"
)

func TestProcessCommandGuardPolarityAndMemo(t *testing.T) {
	for _, state := range []string{process.StateRunning, process.StateAbsent, process.StateUnknown} {
		t.Run(state, func(t *testing.T) {
			calls := 0
			ev := &Evaluator{Deps: checks.Deps{ProcessesAny: func(ctx context.Context, exes []string, user string, cmd *regexp.Regexp) string {
				calls++
				deadline, bounded := ctx.Deadline()
				if !bounded || time.Until(deadline) > 5*time.Second || len(exes) != 0 || user != "" || cmd == nil || !cmd.MatchString("emerge world") {
					t.Errorf("incorrect query: exes=%v user=%q cmd=%v bounded=%v", exes, user, cmd, bounded)
				}
				return state
			}}}
			rule := Rule{Name: "packages", Type: RuleGuard, Blocks: []string{"restart"}, If: map[string]any{
				"not": map[string]any{"process": map[string]any{"cmd": "^emerge ", "state": "absent", "timeout": "5s"}},
			}}
			for range 2 {
				blocking, _, err := Guard(t.Context(), []Rule{rule}, "restart", ev)
				if (blocking != "") != (state == process.StateRunning) || (err != nil) != (state == process.StateUnknown) {
					t.Fatalf("state=%s blocking=%q err=%v", state, blocking, err)
				}
			}
			if calls != 1 {
				t.Fatalf("probes=%d, want one per evaluation cycle", calls)
			}
		})
	}
}

func TestProcessCommandGuardTimeoutCannotProveAbsent(t *testing.T) {
	ev := &Evaluator{Deps: checks.Deps{ProcessesAny: func(ctx context.Context, _ []string, _ string, _ *regexp.Regexp) string {
		<-ctx.Done()
		return process.StateAbsent
	}}}
	rule := Rule{Name: "packages", Type: RuleGuard, Blocks: []string{"restart"}, If: map[string]any{
		"not": map[string]any{"process": map[string]any{"cmd": "emerge", "state": "absent", "timeout": "1ms"}},
	}}
	if _, _, err := Guard(t.Context(), []Rule{rule}, "restart", ev); err == nil {
		t.Fatal("timed-out observation allowed the operation")
	}
}
