package config

import (
	"fmt"
	"strings"
	"testing"

	"sermo/internal/checks"
)

func levelPred(op string, value any) map[string]any {
	return map[string]any{checks.CheckKeyOp: op, checks.CheckKeyValue: value}
}

// diskLadder is a storage check graded warning / error / critical.
func diskLadder() map[string]any {
	return map[string]any{
		"type":     checks.CheckTypeStorage,
		"path":     "/",
		"used_pct": levelPred(">=", "80%"),
		"levels": map[string]any{
			"error":    map[string]any{"used_pct": levelPred(">=", "95%")},
			"critical": map[string]any{"used_pct": levelPred(">=", "99%")},
		},
	}
}

func TestValidateWatchLevels(t *testing.T) {
	t.Run("valid ladder", func(t *testing.T) {
		w := map[string]any{"severity": "warning", "check": diskLadder(), "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}}
		assertNoWatchIssues(t, map[string]any{"watches": map[string]any{"disk-root": w}})
	})
	t.Run("unknown level name", func(t *testing.T) {
		check := diskLadder()
		check["levels"] = map[string]any{"urgent": map[string]any{"used_pct": levelPred(">=", "99%")}}
		assertWatchIssues(t, watchConfig("disk-root", check), "watches.disk-root.check.levels.urgent is not a severity")
	})
	t.Run("unsupported type", func(t *testing.T) {
		check := map[string]any{"type": checks.CheckTypeTCP, "host": "127.0.0.1", "port": 22,
			"levels": map[string]any{"error": map[string]any{"latency": 1}}}
		assertWatchIssues(t, watchConfig("ssh", check), "not supported on a tcp check")
	})
	t.Run("multi-metric check block", func(t *testing.T) {
		w := map[string]any{
			"check": map[string]any{"type": checks.CheckTypeNet, "interface": "enp1s0",
				"levels": map[string]any{"error": map[string]any{"delta": levelPred(">", 500)}}},
			"metrics": map[string]any{"errors": map[string]any{
				"delta": levelPred(">", 100),
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			}},
		}
		assertWatchIssues(t, map[string]any{"watches": map[string]any{"net": w}}, "declare them inside the metric block")
	})
	t.Run("metric block ladder", func(t *testing.T) {
		w := map[string]any{
			"check": map[string]any{"type": checks.CheckTypeNet, "interface": "enp1s0"},
			"metrics": map[string]any{"errors": map[string]any{
				"severity": "warning",
				"delta":    levelPred(">", 100),
				"levels":   map[string]any{"error": map[string]any{"delta": levelPred(">", 500)}},
				"then":     map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			}},
		}
		assertNoWatchIssues(t, map[string]any{"watches": map[string]any{"net": w}})
	})
	t.Run("stateful metric block", func(t *testing.T) {
		w := map[string]any{
			"check": map[string]any{"type": checks.CheckTypeNet, "interface": "enp1s0"},
			"metrics": map[string]any{"state": map[string]any{
				"expect": "up",
				"levels": map[string]any{"critical": map[string]any{"delta": levelPred(">", 1)}},
				"then":   map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			}},
		}
		assertWatchIssues(t, map[string]any{"watches": map[string]any{"net": w}}, "no ordered threshold")
	})
	// A lax tier is not an error: the configuration still loads.
	t.Run("lax tier stays valid", func(t *testing.T) {
		check := diskLadder()
		check["used_pct"] = levelPred(">=", "97%")
		assertNoWatchIssues(t, watchConfig("disk-root", check))
	})
}

func TestValidateServiceLevels(t *testing.T) {
	collect := func(tree map[string]any, section string) []string {
		var issues []string
		validateCheckSection(tree, section, "", func(format string, args ...any) {
			issues = append(issues, fmt.Sprintf(format, args...))
		})
		return issues
	}
	ok := map[string]any{"checks": map[string]any{"disk": diskLadder()}}
	if issues := collect(ok, "checks"); len(issues) != 0 {
		t.Fatalf("valid service ladder: %v", issues)
	}
	preflight := map[string]any{"preflight": map[string]any{"disk": diskLadder()}}
	if issues := collect(preflight, "preflight"); !strings.Contains(strings.Join(issues, "\n"), "not supported on a preflight check") {
		t.Fatalf("preflight levels: %v", issues)
	}
}

func TestValidateServiceWatchSeverityAndLevels(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
unit: svc.service
watches:
  disk:
    severity: urgent
    check:
      type: storage
      path: /
      used_pct: { op: ">=", value: "80%" }
      levels:
        fatal: { used_pct: { op: ">=", value: "99%" } }
    then:
      hook: { command: [/x] }
`)
	mustHave(t, issues, "watches.disk.severity")
	mustHave(t, issues, "watches.disk.check.levels.fatal is not a severity")
}

// A rule-class service watch becomes a check plus a rule; the entry's severity
// must grade that check rather than vanish with the watch.
func TestServiceWatchSeverityGradesTheGeneratedCheck(t *testing.T) {
	tree := map[string]any{"watches": map[string]any{
		"alert-if-memory-high": map[string]any{
			"severity": "warning",
			"check":    map[string]any{"type": checks.CheckTypeMetric, "name": "memory", "op": ">", "value": "30%"},
			"then":     map[string]any{"action": "alert", "message": "memory high"},
		},
		"alert-if-cpu-high": map[string]any{
			"severity": "warning",
			"check":    map[string]any{"type": checks.CheckTypeMetric, "name": "cpu", "op": ">", "value": "90%", "severity": "error"},
			"then":     map[string]any{"action": "alert", "message": "cpu high"},
		},
	}}
	if errs := expandServiceWatches(tree); len(errs) > 0 {
		t.Fatalf("expandServiceWatches: %v", errs)
	}
	generated := nested(t, tree, "checks")
	if got := nested(t, generated, "alert-if-memory-high")["severity"]; got != "warning" {
		t.Fatalf("generated memory check severity = %v, want warning", got)
	}
	if got := nested(t, generated, "alert-if-cpu-high")["severity"]; got != "error" {
		t.Fatalf("generated cpu check severity = %v, want the narrower check-block error", got)
	}
}

// A desugared service watch never reaches the resolved-tree validators: its
// severity is checked on the entry the operator wrote, never copied when
// invalid, and levels beside it (instead of inside check:) are refused rather
// than silently dropped.
func TestDesugaredServiceWatchGrading(t *testing.T) {
	tree := map[string]any{"watches": map[string]any{
		"up": map[string]any{
			"severity": "high",
			"check":    map[string]any{"type": checks.CheckTypeService, "expect": "active"},
		},
		"alert-if-memory-high": map[string]any{
			"severity": "warning",
			"levels":   map[string]any{"error": map[string]any{"value": "50%"}},
			"check":    map[string]any{"type": checks.CheckTypeMetric, "name": "memory", "op": ">", "value": "30%"},
			"then":     map[string]any{"action": "alert", "message": "memory high"},
		},
	}}
	var issues []Issue
	for _, msg := range expandServiceWatches(tree) {
		issues = append(issues, Issue{Msg: msg})
	}
	mustHave(t, issues, `watches.up.severity "high" must be one of`)
	mustNotHave(t, issues, "checks.up.severity")
	mustHave(t, issues, "watches.alert-if-memory-high.levels grade the check's thresholds: declare them inside check")
	if _, copied := nested(t, nested(t, tree, "checks"), "up")["severity"]; copied {
		t.Fatal("an invalid watch severity was copied into the generated check")
	}
}

// A catalog check's levels survive an override that turns it into a sensor:
// they are ignored with a warning, never a reason to refuse the load.
func TestLevelsUnderAnotherReportsAreIgnored(t *testing.T) {
	check := diskLadder()
	check["reports"] = checks.ReportsState
	assertNoWatchIssues(t, watchConfig("disk-root", check))
}

func TestInlineProbeRejectsLevels(t *testing.T) {
	var issues []string
	add := func(format string, args ...any) { issues = append(issues, fmt.Sprintf(format, args...)) }
	probe := map[string]any{checks.CheckTypeStorage: map[string]any{"path": "/", "used_pct": levelPred(">", 90),
		"levels": map[string]any{"critical": map[string]any{"used_pct": levelPred(">", 99)}}}}
	validateProbe(probe, "rules.r.if.active", map[string]struct{}{}, map[string]struct{}{}, false, add)
	if !strings.Contains(strings.Join(issues, "\n"), "not supported on an inline rule probe") {
		t.Fatalf("inline probe levels: %v", issues)
	}
}

// Warnings names the tier an operator's raised threshold left behind, for both
// host watches and services, without failing validation.
func TestWarningsReportIgnoredLevels(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal + `
watches:
  disk-root:
    severity: warning
    check:
      type: storage
      path: /
      used_pct: { op: ">=", value: "97%" }
      levels:
        error:    { used_pct: { op: ">=", value: "95%" } }
        critical: { used_pct: { op: ">=", value: "99%" } }
    then:
      hook: { command: [/x] }
`,
		"services/svc.yml": `
kind: service
name: svc
unit: svc.service
checks:
  queue:
    type: command
    command: [exim, -bpc]
    expect_stdout: { op: "<=", value: 2000 }
    severity: warning
    levels:
      error: { expect_stdout: { op: "<=", value: 1000 } }
watches:
  alert-if-backlog-high:
    severity: warning
    check:
      type: command
      command: [exim, -bpc]
      expect_stdout: { op: "<=", value: 500 }
      levels:
        error: { expect_stdout: { op: "<=", value: 400 } }
    then: { action: alert, message: backlog high }
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, issue := range Validate(cfg) {
		if strings.Contains(issue.Msg, "levels") {
			t.Fatalf("a lax tier failed validation: %v", issue)
		}
	}
	warnings := Warnings(cfg)
	mustHave(t, warnings, "watches.disk-root.check.levels.error.used_pct >= 95 is not stricter than >= 97; ignored")
	mustHave(t, warnings, "checks.queue.levels.error.expect_stdout <= 1000 is not stricter than <= 2000; ignored")
	// A watch promoted to a check and a rule is named as the operator wrote it.
	mustHave(t, warnings, "watches.alert-if-backlog-high.check.levels.error.expect_stdout <= 400 is not stricter than <= 500; ignored")
	mustNotHave(t, warnings, "levels.critical")
}

func TestValidateRuleSeverity(t *testing.T) {
	collect := func(entry map[string]any) []string {
		var issues []string
		validateRule("rules.r", entry, map[string]struct{}{}, map[string]struct{}{"c": {}}, map[string]struct{}{}, func(format string, args ...any) {
			issues = append(issues, fmt.Sprintf(format, args...))
		})
		return issues
	}
	alert := func(level string, typ string) map[string]any {
		return map[string]any{
			"type": typ, "severity": level,
			"if":   map[string]any{"active": map[string]any{"check": "c"}},
			"then": map[string]any{"action": "alert", "message": "m"},
		}
	}
	if issues := collect(alert("critical", "alert")); len(issues) != 0 {
		t.Fatalf("valid rule severity: %v", issues)
	}
	if issues := collect(alert("urgent", "alert")); !strings.Contains(strings.Join(issues, "\n"), "rules.r.severity") {
		t.Fatalf("invalid rule severity: %v", issues)
	}
	guard := alert("error", "guard")
	guard["then"] = map[string]any{"action": "block", "message": "busy"}
	guard["blocks"] = []any{"restart"}
	if issues := collect(guard); !strings.Contains(strings.Join(issues, "\n"), "not supported on a guard rule") {
		t.Fatalf("guard severity: %v", issues)
	}
}
