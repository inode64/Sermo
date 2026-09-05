package config

import (
	"strings"
	"testing"
)

func TestValidateMultiAction(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
checks:
  http: { type: http, url: "http://127.0.0.1/" }
rules:
  ok-multi:
    type: remediation
    if: { failed: { check: http } }
    then:
      actions:
        - { type: alert, message: "down, restarting" }
        - { type: restart }
  bad-multi:
    type: remediation
    if: { failed: { check: http } }
    then:
      actions:
        - { type: alert }
        - { type: explode }
`)
	// The valid multi-action rule must not be flagged.
	mustNotHave(t, issues, "ok-multi")
	mustHave(t, issues, "action alert requires a non-empty message")
	mustHave(t, issues, `then.action "explode" is not one of`)
}

func TestValidateClearWindowSection(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
clear_window: { cycles: 2, min_matches: 1 }
`)
	mustHave(t, issues, "clear_window.min_matches is not supported")

	issues = validateService(t, `
name: svc
service: x
clear_window: { cycles: 2, duration: 4m }
`)
	mustHave(t, issues, "clear_window cannot define both cycles and duration")

	issues = validateService(t, `
name: svc
service: x
clear_window: { cycles: 1 }
`)
	mustNotHave(t, issues, "clear_window")
}

func TestValidateSystemMetricOnlyInAlert(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
rules:
  bad:
    type: remediation
    if: { metric: { scope: system, name: total_cpu, op: ">", value: 90% } }
    then: { action: restart }
  bad-inline:
    type: remediation
    if: { failed: { metric: { scope: system, name: total_cpu, op: ">", value: 90% } } }
    then: { action: restart }
  ok-alert:
    type: alert
    if: { metric: { scope: system, name: total_cpu, op: ">", value: 90% } }
    then: { action: alert, message: "machine hot" }
  ok-alert-inline:
    type: alert
    if: { failed: { metric: { scope: system, name: total_cpu, op: ">", value: 90% } } }
    then: { action: alert, message: "machine hot" }
`)
	mustHave(t, issues, "scope: system metric is only allowed in alert rules")
	// The alert rule's identical condition must NOT be flagged.
	for _, is := range issues {
		if strings.Contains(is.Msg, "ok-alert") && strings.Contains(is.Msg, "system metric") {
			t.Fatalf("alert rule wrongly flagged: %v", is)
		}
	}
}

func TestValidateMetricFormValidCombinations(t *testing.T) {
	// memory has both forms; cpu accepts %; load accepts absolute.
	issues := validateService(t, `
name: svc
service: x
rules:
  mem-pct: { type: alert, if: { metric: { name: memory, op: ">", value: 40% } }, then: { action: alert, message: m } }
  mem-abs: { type: alert, if: { metric: { name: memory, op: ">", value: 1000000 } }, then: { action: alert, message: m } }
  cpu-pct: { type: alert, if: { metric: { name: cpu, op: ">", value: 80% } }, then: { action: alert, message: m } }
  load:    { type: alert, if: { metric: { scope: system, name: load1, op: ">", value: 4 } }, then: { action: alert, message: m } }
`)
	for _, is := range issues {
		if strings.Contains(is.Msg, "threshold") && strings.Contains(is.Msg, "form") {
			t.Fatalf("valid metric form wrongly flagged: %v", is)
		}
	}
}

func TestValidateIndirectSystemMetricInRemediation(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
checks:
  machine-hot: { type: metric, scope: system, name: total_cpu, op: ">", value: 90% }
rules:
  bad:
    type: remediation
    if: { active: { check: machine-hot } }
    then: { action: restart }
  ok-alert:
    type: alert
    if: { active: { check: machine-hot } }
    then: { action: alert, message: m }
`)
	mustHave(t, issues, `references system metric check "machine-hot", which is only allowed in alert rules`)
	// The alert rule referencing the same check must not be flagged.
	for _, is := range issues {
		if strings.Contains(is.Msg, "ok-alert") && strings.Contains(is.Msg, "system metric") {
			t.Fatalf("alert rule wrongly flagged: %v", is)
		}
	}
}

func TestValidateSystemTotalSwapMetric(t *testing.T) {
	good := validateService(t, `
name: svc
service: x
rules:
  swap-alert:
    type: alert
    if: { metric: { scope: system, name: total_swap, op: ">", value: 80% } }
    then: { action: alert, message: "swap high" }
`)
	if len(good) != 0 {
		t.Fatalf("total_swap should be in the system metric catalog, got %v", good)
	}
}

// TestValidateRuleTypeActionCoupling sharpens the rule-type distinction:
// operation actions belong to remediation rules only, and a remediation rule
// must carry one — an alert-only remediation (or an alert rule with a restart)
// would otherwise validate and then silently not do what it reads like.
func TestValidateRuleTypeActionCoupling(t *testing.T) {
	rule := func(rtype, then string) string {
		return "name: svc\nservice: x\nchecks:\n  c: { type: tcp, host: 127.0.0.1, port: 80 }\nrules:\n  r:\n    type: " + rtype + "\n    if: { failed: { check: c } }\n" + then
	}
	mustHave(t, validateService(t, rule("remediation", "    then: { action: alert, message: m }\n")),
		"remediation requires an operation action")
	mustHave(t, validateService(t, rule("alert", "    then: { action: restart }\n")),
		"only remediation rules may use action restart")
	mustHave(t, validateService(t, rule("guard", "    blocks: [restart]\n    then:\n      actions: [ { type: block, message: m }, { type: stop } ]\n")),
		"only remediation rules may use action stop")
	if issues := validateService(t, rule("remediation", "    then: { action: reload }\n")); hasIssue(issues, "rules.r") {
		t.Fatalf("a reload remediation must be valid, got %v", issues)
	}
	if issues := validateService(t, rule("remediation", "    then: { action: resume }\n")); hasIssue(issues, "rules.r") {
		t.Fatalf("a resume remediation must be valid, got %v", issues)
	}
}
