package config

import (
	"context"
	"testing"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/rules"
)

func TestAlloyCatalogOTLPRequiresOptIn(t *testing.T) {
	for _, backend := range []string{"systemd", "openrc"} {
		for _, enabled := range []bool{false, true} {
			name := backend + "/default"
			body := "name: alloy-main\nuses: alloy\n"
			if enabled {
				name = backend + "/enabled"
				body += "watches:\n  otlp: {enabled: true}\n"
			}
			t.Run(name, func(t *testing.T) {
				global := writeConfig(t, map[string]string{
					"sermo.yml":          "engine: {backend: " + backend + "}\npaths: {services: [@ROOT@/services]}\ndefaults: {policy: {cooldown: 5m}}\n",
					"services/alloy.yml": body,
				})
				cfg, err := loadConfig(t, global, WithCatalogDirs(repoCatalogDir(repoRoot(t))))
				if err != nil {
					t.Fatal(err)
				}
				if issues := Validate(cfg); len(issues) != 0 {
					t.Fatal(issues)
				}
				resolved, errs := cfg.Resolve("alloy-main")
				if len(errs) != 0 {
					t.Fatal(errs)
				}
				_, hasCheck := nested(t, resolved.Tree, "checks")["otlp"]
				_, hasRule := nested(t, resolved.Tree, "rules")["otlp"]
				if hasCheck != enabled || hasRule != enabled {
					t.Fatalf("opt-in=%v: check=%v rule=%v", enabled, hasCheck, hasRule)
				}
				if enabled {
					assertAlloyOTLPAlerts(t, resolved.Tree)
				}
			})
		}
	}
}

func assertAlloyOTLPAlerts(t *testing.T, tree map[string]any) {
	t.Helper()
	parsed, warnings := rules.ParseRules(tree)
	if len(warnings) != 0 {
		t.Fatal(warnings)
	}
	for _, rule := range parsed {
		if rule.Name != "otlp" {
			continue
		}
		for _, tc := range []struct {
			name   string
			result checks.Result
			want   bool
		}{
			{"healthy", checks.Result{OK: true, Optional: true}, false},
			{"unhealthy", checks.Result{Optional: true}, true},
			{"unavailable", checks.Result{Optional: true, Unavailable: true}, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				evaluator := rules.Evaluator{Cache: map[string]checks.Result{"otlp": tc.result}}
				active, err := evaluator.Eval(context.Background(), rule.If)
				if err != nil {
					t.Fatal(err)
				}
				var window rules.WindowState
				at := time.Unix(1_700_000_000, 0)
				if window.FiresAt(rule, active, at) {
					t.Fatal("OTLP alert fired before its two-minute window")
				}
				if got := window.FiresAt(rule, active, at.Add(2*time.Minute)); got != tc.want {
					t.Fatalf("OTLP alert firing=%v, want %v", got, tc.want)
				}
			})
		}
		return
	}
	t.Fatal("enabled OTLP alert rule missing")
}

// TestAlloyCatalogRestartsOnSaturation pins the remediation shape adopted after
// the 23-sep-2026 recurrence: an Alloy that leaked its descriptors alerted for
// hours on fr1 and ca1 while the profile only knew how to alert, and the
// operator's own restart left the previous incarnation alive because no
// selector could name it. Failed readiness now restarts, while FD saturation
// only alerts by default. The restart can clear its residuals, and the policy
// bounds the retries.
func TestAlloyCatalogRestartsOnSaturation(t *testing.T) {
	root := repoRoot(t)
	body := catalogDocByName(t, root, "services", "alloy")
	assertConservativeRemediationPolicy(t, "alloy", body)
	if got := cfgval.String(nested(t, body, "stop_policy")["force_kill"]); got != "auto" {
		t.Fatalf("alloy stop_policy.force_kill = %q, want auto", got)
	}
	reap := nested(t, body, "reap", "kill_only_if")
	if len(cfgval.StringList(reap["users"])) == 0 || len(cfgval.StringList(reap["exe_any"])) == 0 {
		t.Fatalf("alloy reap.kill_only_if must pair users and exe_any: %v", reap)
	}

	for _, tc := range []struct{ backend, user string }{
		{backend: "systemd", user: "alloy"},
		{backend: "openrc", user: "root"},
	} {
		t.Run(tc.backend, func(t *testing.T) {
			global := writeConfig(t, map[string]string{
				"sermo.yml":          "engine: {backend: " + tc.backend + "}\npaths: {services: [@ROOT@/services]}\ndefaults: {policy: {cooldown: 5m}}\n",
				"services/alloy.yml": "name: alloy-main\nuses: alloy\nwatches:\n  otlp: {enabled: true}\n",
			})
			cfg, err := loadConfig(t, global, WithCatalogDirs(repoCatalogDir(root)))
			if err != nil {
				t.Fatal(err)
			}
			if issues := Validate(cfg); len(issues) != 0 {
				t.Fatal(issues)
			}
			resolved, errs := cfg.Resolve("alloy-main")
			if len(errs) != 0 {
				t.Fatal(errs)
			}

			procs := nested(t, resolved.Tree, "processes")
			if len(procs) != 1 {
				t.Fatalf("processes = %v, want exactly the selector of the active init backend", procs)
			}
			for name, raw := range procs {
				sel, _ := raw.(map[string]any)
				if got := cfgval.String(sel["exe"]); got != "/usr/bin/alloy" {
					t.Fatalf("processes.%s.exe = %q, want /usr/bin/alloy", name, got)
				}
				if got := cfgval.String(sel["user"]); got != tc.user {
					t.Fatalf("processes.%s.user = %q, want %q", name, got, tc.user)
				}
			}
			if !cfgval.Bool(nested(t, resolved.Tree, "checks", "ready")["verify"]) {
				t.Fatal("checks.ready must verify the restart (verify: true)")
			}

			parsed, warnings := rules.ParseRules(resolved.Tree)
			if len(warnings) != 0 {
				t.Fatal(warnings)
			}
			for _, want := range []string{"ready", "otlp"} {
				rule := ruleByName(t, parsed, want)
				if rule.Type != rules.RuleRemediation {
					t.Fatalf("rule %s type = %s, want %s", want, rule.Type, rules.RuleRemediation)
				}
				var restarts, explained bool
				for _, action := range rule.Actions {
					if action.Type == rules.ActionRestart {
						restarts = true
					}
					if action.Message != "" {
						explained = true
					}
				}
				if !restarts || !explained {
					t.Fatalf("rule %s actions = %v, want a restart and a message telling the operator why", want, rule.Actions)
				}
			}
			fds := ruleByName(t, parsed, "restart-if-fds-high")
			if fds.Type != rules.RuleAlert || len(fds.Actions) != 1 || fds.Actions[0].Type != rules.ActionAlert {
				t.Fatalf("FD saturation must alert without restart permission, got %+v", fds)
			}
			if _, stale := nested(t, resolved.Tree, "rules")["alert-if-fds-high"]; stale {
				t.Fatal("alert-if-fds-high must not survive next to the injected restart-if-fds-high")
			}
			if got := cfgval.String(nested(t, resolved.Tree, "checks", "fds")["value"]); got != "80%" {
				t.Fatalf("checks.fds.value = %q, want the injected default 80%%", got)
			}
		})
	}
}

func ruleByName(t *testing.T, parsed []rules.Rule, name string) rules.Rule {
	t.Helper()
	for _, rule := range parsed {
		if rule.Name == name {
			return rule
		}
	}
	t.Fatalf("rule %s missing", name)
	return rules.Rule{}
}
