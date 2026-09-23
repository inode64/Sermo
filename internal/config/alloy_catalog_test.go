package config

import (
	"context"
	"testing"
	"time"

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
