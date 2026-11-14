package config

import (
	"fmt"
	"slices"
	"testing"

	"sermo/internal/cfgval"
	"sermo/internal/rules"
)

// TestCatalogRPCStatdRepairsLostRegistration pins the rpc-statd repair: a
// restarted rpcbind forgets statd's registration while statd keeps running, so
// the profile looks statd up through rpcbind, restarts it when the lookup
// fails, and refuses to start it while the portmapper is down. A generated
// override that still disables the retired port-662 `statd` watch must not
// switch the repair off.
func TestCatalogRPCStatdRepairsLostRegistration(t *testing.T) {
	t.Parallel()
	const name = "restart-if-statd-failed"
	for _, tc := range []struct{ label, extra string }{
		{label: "catalog"},
		{label: "retired statd override", extra: "watches:\n  statd:\n    enabled: false\n"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			resolved := resolveCatalogService(t, "rpc-statd", backendOpenRC, tc.extra)

			check := nested(t, resolved.Tree, sectionChecks, name)
			for key, want := range map[string]string{
				"type": "rpcbind", "host": "127.0.0.1", "transport": "tcp", "program": "100024", "version": "1", "verify": "true",
			} {
				if got := fmt.Sprint(check[key]); got != want {
					t.Fatalf("check %s = %q, want %q (check=%v)", key, got, want, check)
				}
			}

			rule := nested(t, resolved.Tree, rules.SectionRules, name)
			if got := cfgval.String(nested(t, rule, rules.RuleFieldThen)[rules.RuleFieldAction]); got != "restart" {
				t.Fatalf("rule action = %q, want restart (rule=%v)", got, rule)
			}
			if got := cfgval.String(nested(t, rule, "if", "failed")["check"]); got != name {
				t.Fatalf("rule condition check = %q, want %q (rule=%v)", got, name, rule)
			}
			if got := fmt.Sprint(nested(t, rule, "for")["cycles"]); got != "3" {
				t.Fatalf("rule for.cycles = %q, want 3 (rule=%v)", got, rule)
			}

			guard := nested(t, resolved.Tree, rules.SectionRules, "block-without-portmapper")
			if got := cfgval.String(guard["type"]); got != "guard" {
				t.Fatalf("block-without-portmapper type = %q, want guard", got)
			}
			if got := cfgval.StringList(guard["blocks"]); !slices.Equal(got, []string{"start", "restart", "resume"}) {
				t.Fatalf("guard blocks = %v, want start, restart and resume", got)
			}
			portmapper := nested(t, guard, "if", "not", "active", "rpcbind")
			if got := fmt.Sprint(portmapper["program"]); got != "100000" {
				t.Fatalf("guard probes program %q, want the portmapper 100000", got)
			}
		})
	}
}
