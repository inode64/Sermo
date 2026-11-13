package config

import (
	"slices"
	"testing"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/conn"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
)

func TestCatalogACPIDLifecycle(t *testing.T) {
	for _, backend := range []string{backendSystemd, backendOpenRC} {
		t.Run(backend, func(t *testing.T) {
			resolved := resolveCatalogService(t, conn.ProtocolNameACPID, backend)
			lifecycle := ResolveServiceLifecycle(resolved.Tree, backend)
			var want []AuxiliaryUnit
			if backend == backendSystemd {
				want = []AuxiliaryUnit{{Unit: "acpid.socket", Optional: true}}
			}
			if !slices.Equal(lifecycle.AuxiliaryUnits, want) {
				t.Fatalf("auxiliary units = %+v, want %+v", lifecycle.AuxiliaryUnits, want)
			}
			probe := nested(t, resolved.Tree, SectionChecks, conn.ProtocolNameACPID)
			if probe[checks.CheckKeyType] != conn.ProtocolNameACPID || !cfgval.Bool(probe[checks.CheckKeyVerify]) {
				t.Fatalf("postflight must verify the ACPI socket, got %v", probe)
			}
		})
	}
}

func TestCatalogACPIDRecoversBrokenSocketWhileIdle(t *testing.T) {
	for _, backend := range []string{backendSystemd, backendOpenRC} {
		t.Run(backend, func(t *testing.T) {
			resolved := resolveCatalogService(t, conn.ProtocolNameACPID, backend)
			parsed, warnings := rules.ParseRules(resolved.Tree)
			if len(warnings) != 0 {
				t.Fatal(warnings)
			}
			rule := ruleByName(t, parsed, conn.ProtocolNameACPID)
			if rule.Type != rules.RuleRemediation || len(rule.Actions) != 1 || rule.Actions[0].Type != rules.ActionRestart {
				t.Fatalf("socket failure must request a guarded restart, got %+v", rule)
			}
			for _, tc := range []struct {
				name string
				bad  checks.Result
				want []int
			}{
				{name: "refused connection", bad: checks.Result{OK: false}, want: []int{3, 6}},
				{name: "healthy", bad: checks.Result{OK: true}},
				{name: "busy", bad: checks.Result{OK: false, Busy: true}},
				{name: "skipped", bad: checks.Result{OK: false, Skipped: true}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var window rules.WindowState
					var fired []int
					for cycle, probe := range []checks.Result{tc.bad, {OK: true}, tc.bad, tc.bad, {OK: true}, tc.bad, tc.bad} {
						eval := rules.Evaluator{Cache: map[string]checks.Result{
							checks.CheckTypeService: {OK: true, Data: map[string]any{checks.DataKeyStatus: string(servicemgr.StatusIdle)}},
							conn.ProtocolNameACPID:  probe,
						}}
						match, err := eval.EvalRule(t.Context(), rule)
						if err != nil {
							t.Fatal(err)
						}
						if window.FiresAt(rule, match, time.Unix(int64(cycle), 0)) {
							fired = append(fired, cycle)
						}
					}
					if !slices.Equal(fired, tc.want) {
						t.Fatalf("restart cycles = %v, want %v", fired, tc.want)
					}
				})
			}
		})
	}
}
