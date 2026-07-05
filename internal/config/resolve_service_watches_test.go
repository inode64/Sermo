package config

import (
	"reflect"
	"strings"
	"testing"

	"sermo/internal/cfgval"
)

// resolveWatchService loads a single-service config whose body is `body` and
// resolves it, returning the resolved tree and any errors.
func resolveWatchService(t *testing.T, body string) (map[string]any, []string) {
	t.Helper()
	global := writeConfig(t, map[string]string{
		"sermo.yml":        baseGlobal,
		"services/svc.yml": "name: svc\nservice: x\npolicy: { cooldown: 5m }\n" + body,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	resolved, errs := cfg.Resolve("svc")
	return resolved.Tree, errs
}

func TestExpandServiceWatchesRemediation(t *testing.T) {
	tree, errs := resolveWatchService(t, `
watches:
  restart-if-tcp:
    check: { type: tcp, host: 127.0.0.1, port: 80 }
    for: { cycles: 3 }
    then: { action: restart }
`)
	if len(errs) != 0 {
		t.Fatalf("resolve errors = %v", errs)
	}
	// The watch is gone; a generated check + remediation rule take its name.
	if _, ok := tree["watches"]; ok {
		t.Fatalf("watches should be removed after desugar, got %v", tree["watches"])
	}
	chk := nested(t, tree, "checks", "restart-if-tcp")
	if got := cfgval.String(chk["type"]); got != "tcp" {
		t.Fatalf("generated check type = %q, want tcp", got)
	}
	rule := nested(t, tree, "rules", "restart-if-tcp")
	if got := cfgval.String(rule["type"]); got != "remediation" {
		t.Fatalf("rule type = %q, want remediation", got)
	}
	// Health check → failed polarity, referencing the generated check by name.
	failed := nested(t, rule, "if", "failed")
	if got := cfgval.String(failed["check"]); got != "restart-if-tcp" {
		t.Fatalf("rule if.failed.check = %q, want restart-if-tcp", got)
	}
	forWin := nested(t, rule, "for")
	if got := cfgval.String(forWin["cycles"]); got != "3" {
		t.Fatalf("rule for.cycles = %q, want 3 (copied from watch)", got)
	}
	then := nested(t, rule, "then")
	if got := cfgval.String(then["action"]); got != "restart" {
		t.Fatalf("rule then.action = %q, want restart", got)
	}
}

func TestExpandServiceWatchesConditionPolarity(t *testing.T) {
	// A metric (condition-style) check fires on its threshold → `active` polarity.
	tree, errs := resolveWatchService(t, `
watches:
  restart-if-cpu-hot:
    check: { type: metric, scope: service, name: cpu_thread, op: ">", value: "90%" }
    for: { duration: 6m }
    then: { action: restart }
`)
	if len(errs) != 0 {
		t.Fatalf("resolve errors = %v", errs)
	}
	rule := nested(t, tree, "rules", "restart-if-cpu-hot")
	if _, ok := rule["if"].(map[string]any)["active"]; !ok {
		t.Fatalf("condition check should desugar to `active` polarity, got if = %v", rule["if"])
	}
	active := nested(t, rule, "if", "active")
	if got := cfgval.String(active["check"]); got != "restart-if-cpu-hot" {
		t.Fatalf("if.active.check = %q, want restart-if-cpu-hot", got)
	}
}

func TestExpandServiceWatchesGuard(t *testing.T) {
	tree, errs := resolveWatchService(t, `
watches:
  block-restart-if-tcp-down:
    check: { type: tcp, host: 127.0.0.1, port: 80 }
    then: { action: block, blocks: [restart, start], message: "tcp down" }
`)
	if len(errs) != 0 {
		t.Fatalf("resolve errors = %v", errs)
	}
	rule := nested(t, tree, "rules", "block-restart-if-tcp-down")
	if got := cfgval.String(rule["type"]); got != "guard" {
		t.Fatalf("rule type = %q, want guard", got)
	}
	if got := cfgval.StringList(rule["blocks"]); len(got) != 2 || got[0] != "restart" || got[1] != "start" {
		t.Fatalf("rule blocks = %v, want [restart start] at the rule level", got)
	}
	then := nested(t, rule, "then")
	if got := cfgval.String(then["message"]); got != "tcp down" {
		t.Fatalf("then.message = %q, want carried through", got)
	}
}

func TestExpandServiceWatchesAlert(t *testing.T) {
	tree, errs := resolveWatchService(t, `
watches:
  alert-if-fds-high:
    check: { type: metric, scope: service, name: fds, op: ">", value: 50000 }
    within: { cycles: 10, min_matches: 3 }
    then: { action: alert, message: "fds high", notify: [ops] }
`)
	if len(errs) != 0 {
		t.Fatalf("resolve errors = %v", errs)
	}
	rule := nested(t, tree, "rules", "alert-if-fds-high")
	if got := cfgval.String(rule["type"]); got != "alert" {
		t.Fatalf("rule type = %q, want alert", got)
	}
	// notify is an entry-level rule field, not part of then.
	if got := cfgval.StringList(rule["notify"]); len(got) != 1 || got[0] != "ops" {
		t.Fatalf("rule notify = %v, want [ops] at the rule level", got)
	}
	within := nested(t, rule, "within")
	if got := cfgval.String(within["min_matches"]); got != "3" {
		t.Fatalf("within.min_matches = %q, want 3 (copied)", got)
	}
}

func TestExpandServiceWatchesRefSharedCheck(t *testing.T) {
	// A watch may reference an existing shared check by name instead of embedding a
	// probe, so a verify:true/display check is not duplicated.
	tree, errs := resolveWatchService(t, `
checks:
  http: { type: http, url: "http://x/", verify: true }
watches:
  restart-if-http-failed:
    check: { ref: http }
    for: { cycles: 3 }
    then: { action: restart }
`)
	if len(errs) != 0 {
		t.Fatalf("resolve errors = %v", errs)
	}
	// The shared http check stays; no duplicate is generated.
	if _, ok := tree["checks"].(map[string]any)["restart-if-http-failed"]; ok {
		t.Fatalf("ref must not generate a duplicate check")
	}
	http := nested(t, tree, "checks", "http")
	if !cfgval.Bool(http["verify"]) {
		t.Fatalf("shared check must be preserved intact, got %v", http)
	}
	rule := nested(t, tree, "rules", "restart-if-http-failed")
	failed := nested(t, rule, "if", "failed")
	if got := cfgval.String(failed["check"]); got != "http" {
		t.Fatalf("rule must reference the shared check, if.failed.check = %q, want http", got)
	}
}

func TestExpandServiceWatchesRefUnknown(t *testing.T) {
	_, errs := resolveWatchService(t, `
watches:
  restart-if-x:
    check: { ref: nonexistent }
    then: { action: restart }
`)
	if !hasIssueSubstr(errs, "does not name a checks: or preflight: entry") {
		t.Fatalf("ref to a missing check should error, got %v", errs)
	}
}

func TestExpandServiceWatchesLeavesFireAndForget(t *testing.T) {
	// A watch with a fire-and-forget then (hook/notify) is NOT desugared.
	tree, errs := resolveWatchService(t, `
watches:
  worker-count:
    check: { type: process_count, count: { op: ">", value: 40 } }
    for: { cycles: 5 }
    then: { notify: [ops] }
`)
	if len(errs) != 0 {
		t.Fatalf("resolve errors = %v", errs)
	}
	if _, ok := tree["watches"].(map[string]any)["worker-count"]; !ok {
		t.Fatalf("fire-and-forget watch must remain under watches:, got %v", tree["watches"])
	}
	if _, ok := tree["rules"]; ok {
		t.Fatalf("fire-and-forget watch must not generate a rule, got rules = %v", tree["rules"])
	}
}

func TestExpandServiceWatchesNameCollision(t *testing.T) {
	_, errs := resolveWatchService(t, `
checks:
  dup: { type: http, url: "http://x/" }
watches:
  dup:
    check: { type: tcp, host: 127.0.0.1, port: 80 }
    then: { action: restart }
`)
	if !hasIssueSubstr(errs, "would overwrite existing check") {
		t.Fatalf("collision with an existing check should error, got %v", errs)
	}
}

// TestUnifiedWatchEquivalence proves a unified watch desugars to exactly the
// checks:+rules: a hand-written service would declare (same names), so the Worker
// and operation engine treat them identically.
func TestUnifiedWatchEquivalence(t *testing.T) {
	unified, errs := resolveWatchService(t, `
watches:
  restart-if-tcp:
    check: { type: tcp, host: 127.0.0.1, port: 80 }
    for: { cycles: 3 }
    then: { action: restart }
`)
	if len(errs) != 0 {
		t.Fatalf("unified resolve errors = %v", errs)
	}
	explicit, errs := resolveWatchService(t, `
checks:
  restart-if-tcp: { type: tcp, host: 127.0.0.1, port: 80 }
rules:
  restart-if-tcp:
    type: remediation
    if: { failed: { check: restart-if-tcp } }
    for: { cycles: 3 }
    then: { action: restart }
`)
	if len(errs) != 0 {
		t.Fatalf("explicit resolve errors = %v", errs)
	}
	if !reflect.DeepEqual(unified["checks"], explicit["checks"]) {
		t.Fatalf("checks differ:\n unified=%#v\n explicit=%#v", unified["checks"], explicit["checks"])
	}
	if !reflect.DeepEqual(unified["rules"], explicit["rules"]) {
		t.Fatalf("rules differ:\n unified=%#v\n explicit=%#v", unified["rules"], explicit["rules"])
	}
}

func TestValidateUnifiedWatchActions(t *testing.T) {
	base := "name: svc\nservice: x\npolicy: { cooldown: 5m }\n"
	cases := []struct{ name, body, want string }{
		{"action+hook mixing", `watches:
  w:
    check: { type: tcp, host: 127.0.0.1, port: 80 }
    then: { action: restart, hook: { command: [echo] } }
`, "cannot be combined with an action"},
		{"block without blocks", `watches:
  w:
    check: { type: tcp, host: 127.0.0.1, port: 80 }
    then: { action: block }
`, "requires a non-empty blocks"},
		{"blocks without block", `watches:
  w:
    check: { type: tcp, host: 127.0.0.1, port: 80 }
    then: { action: restart, blocks: [restart] }
`, "blocks is only valid with action: block"},
		{"invalid action", `watches:
  w:
    check: { type: tcp, host: 127.0.0.1, port: 80 }
    then: { action: reboot }
`, "is not one of restart"},
		{"blocks entry not an operation", `watches:
  w:
    check: { type: tcp, host: 127.0.0.1, port: 80 }
    then: { action: block, blocks: [alert] }
`, "must be an operation action"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mustHave(t, validateService(t, base+tc.body), tc.want)
		})
	}
}

// TestUnifiedWatchRefEquivalence proves a ref watch desugars to exactly the
// hand-written rule referencing the same shared check — the dominant migration form.
func TestUnifiedWatchRefEquivalence(t *testing.T) {
	unified, errs := resolveWatchService(t, `
checks:
  tcp: { type: tcp, host: 127.0.0.1, port: 80 }
watches:
  restart-if-tcp-failed:
    check: { ref: tcp }
    for: { cycles: 3 }
    then: { action: restart }
`)
	if len(errs) != 0 {
		t.Fatalf("unified resolve errors = %v", errs)
	}
	explicit, errs := resolveWatchService(t, `
checks:
  tcp: { type: tcp, host: 127.0.0.1, port: 80 }
rules:
  restart-if-tcp-failed:
    type: remediation
    if: { failed: { check: tcp } }
    for: { cycles: 3 }
    then: { action: restart }
`)
	if len(errs) != 0 {
		t.Fatalf("explicit resolve errors = %v", errs)
	}
	if !reflect.DeepEqual(unified["checks"], explicit["checks"]) {
		t.Fatalf("checks differ:\n unified=%#v\n explicit=%#v", unified["checks"], explicit["checks"])
	}
	if !reflect.DeepEqual(unified["rules"], explicit["rules"]) {
		t.Fatalf("rules differ:\n unified=%#v\n explicit=%#v", unified["rules"], explicit["rules"])
	}
}

// TestUnifiedWatchCoexistsWithRules covers a service carrying both a kept rule
// (a shape the desugar does not migrate) and a migrated watch: the desugar must
// append to the existing rules map, not clobber it.
func TestUnifiedWatchCoexistsWithRules(t *testing.T) {
	tree, errs := resolveWatchService(t, `
rules:
  restart-if-lib-changed:
    type: remediation
    if: { changed: { path: /usr/lib/x.so } }
    then: { action: restart }
watches:
  restart-if-tcp-failed:
    check: { type: tcp, host: 127.0.0.1, port: 80 }
    for: { cycles: 3 }
    then: { action: restart }
`)
	if len(errs) != 0 {
		t.Fatalf("resolve errors = %v", errs)
	}
	rules := nested(t, tree, "rules")
	if _, ok := rules["restart-if-lib-changed"]; !ok {
		t.Fatalf("the kept changed-rule must survive, got %v", rules)
	}
	if _, ok := rules["restart-if-tcp-failed"]; !ok {
		t.Fatalf("the migrated watch must desugar into the same rules map, got %v", rules)
	}
}

func hasIssueSubstr(errs []string, sub string) bool {
	for _, e := range errs {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}
