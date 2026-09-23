package config

import (
	"maps"
	"strings"
	"testing"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/rules"
)

func fdsTree(extra map[string]any) map[string]any {
	tree := map[string]any{
		keyName:        "nginx",
		keyDisplayName: "Nginx",
		"processes": map[string]any{
			"main": map[string]any{"exe": "/usr/sbin/nginx", "user": "root"},
		},
	}
	maps.Copy(tree, extra)
	return tree
}

func fdsGenerated(t *testing.T, tree map[string]any) (map[string]any, map[string]any) {
	t.Helper()
	if errs := expandFDs(tree); len(errs) > 0 {
		t.Fatalf("expandFDs: %v", errs)
	}
	checksMap, _ := tree[sectionChecks].(map[string]any)
	check, _ := checksMap[fdsCheckName].(map[string]any)
	ruleMap, _ := tree[rules.SectionRules].(map[string]any)
	rule, _ := ruleMap[fdsRuleName].(map[string]any)
	return check, rule
}

// Absent keys mean the default threshold and a restart, the convention
// restart_on_stale_binary uses.
func TestFDsDefaultInjectsMetricAndRestart(t *testing.T) {
	tree := fdsTree(nil)
	check, rule := fdsGenerated(t, tree)
	if check == nil || rule == nil {
		t.Fatalf("fds check/rule not injected: %v", tree)
	}
	for key, want := range map[string]string{
		checks.CheckKeyType:  checks.CheckTypeMetric,
		checks.CheckKeyScope: checks.MetricScopeService,
		checks.CheckKeyName:  "fds",
		checks.CheckKeyOp:    ">",
		checks.CheckKeyValue: "80%",
	} {
		if got := cfgval.String(check[key]); got != want {
			t.Fatalf("checks.fds.%s = %q, want %q", key, got, want)
		}
	}
	if !cfgval.Bool(check[checks.CheckKeyOptional]) {
		t.Fatal("checks.fds must be optional: an unreadable limit is a warning, not a failed service")
	}
	if got := rule[rules.RuleFieldType]; got != string(rules.RuleRemediation) {
		t.Fatalf("rule type = %v, want remediation", got)
	}
	if got := ruleActionTypes(t, rule); len(got) != 2 || got[0] != "alert" || got[1] != "restart" {
		t.Fatalf("want alert then restart, got %v", got)
	}
	cond := nested(t, rule, rules.RuleFieldIf, rules.ConditionActive)
	if cfgval.String(cond[rules.FieldCheck]) != fdsCheckName {
		t.Fatalf("rule condition = %v, want active fds", rule[rules.RuleFieldIf])
	}
	if got := cfgval.String(nested(t, rule, rules.RuleFieldFor)[rules.WindowKeyDuration]); got != "3m" {
		t.Fatalf("rule for.duration = %q, want 3m", got)
	}
	then := nested(t, rule, rules.RuleFieldThen)
	alert, _ := then[rules.RuleFieldActions].([]any)[0].(map[string]any)
	msg := cfgval.String(alert[rules.RuleFieldMessage])
	for _, want := range []string{"Nginx", "${check.threshold}", "${check.value}", "${rule.duration}"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("alert message %q lacks %q", msg, want)
		}
	}
	for _, key := range []string{keyFDsLimit, keyRestartOnFDsHigh} {
		if _, left := tree[key]; left {
			t.Fatalf("%s must be consumed by the sugar", key)
		}
	}
}

func TestFDsLimitTunesThreshold(t *testing.T) {
	check, _ := fdsGenerated(t, fdsTree(map[string]any{keyFDsLimit: "90%"}))
	if got := cfgval.String(check[checks.CheckKeyValue]); got != "90%" {
		t.Fatalf("checks.fds.value = %q, want 90%%", got)
	}
}

func TestFDsLimitFalseInjectsNothing(t *testing.T) {
	tree := fdsTree(map[string]any{keyFDsLimit: false})
	check, rule := fdsGenerated(t, tree)
	if check != nil || rule != nil {
		t.Fatalf("fds_limit: false must inject nothing, got check=%v rule=%v", check, rule)
	}
}

func TestFDsRestartFalseKeepsAlert(t *testing.T) {
	_, rule := fdsGenerated(t, fdsTree(map[string]any{keyRestartOnFDsHigh: false}))
	if got := rule[rules.RuleFieldType]; got != string(rules.RuleAlert) {
		t.Fatalf("rule type = %v, want alert", got)
	}
	if got := ruleActionTypes(t, rule); len(got) != 1 || got[0] != "alert" {
		t.Fatalf("want alert only, got %v", got)
	}
}

func TestFDsRejectsMalformedKeys(t *testing.T) {
	for name, extra := range map[string]map[string]any{
		"limit true":       {keyFDsLimit: true},
		"limit absolute":   {keyFDsLimit: "50000"},
		"limit text":       {keyFDsLimit: "lots"},
		"restart not bool": {keyRestartOnFDsHigh: "yes"},
	} {
		t.Run(name, func(t *testing.T) {
			if errs := expandFDs(fdsTree(extra)); len(errs) == 0 {
				t.Fatalf("%v must be rejected", extra)
			}
		})
	}
}

func TestFDsSkipsServicesTheMetricCannotDescribe(t *testing.T) {
	for name, tree := range map[string]map[string]any{
		"explicitly no processes":            {keyName: "oneshot", "processes": map[string]any{}},
		"external control without processes": {keyName: "vm", SectionControl: map[string]any{"type": "libvirt"}},
		"delegated workload": {keyName: "docker", "processes": map[string]any{
			"main":  map[string]any{"exe": "/usr/bin/dockerd", "user": "root"},
			"shims": map[string]any{"exe": "/usr/bin/containerd-shim", "user": "root", "delegated": true},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			check, rule := fdsGenerated(t, tree)
			if check != nil || rule != nil {
				t.Fatalf("expected no fds sensor, got check=%v rule=%v", check, rule)
			}
		})
	}
	t.Run("init-attributed service without selectors", func(t *testing.T) {
		check, rule := fdsGenerated(t, map[string]any{keyName: "exim", "service": "exim"})
		if check == nil || rule == nil {
			t.Fatal("a service whose processes the init backend names must get the sensor")
		}
	})
}

func TestFDsRefusesToShadowOperatorEntries(t *testing.T) {
	tree := fdsTree(map[string]any{sectionChecks: map[string]any{fdsCheckName: map[string]any{"type": "metric"}}})
	errs := expandFDs(tree)
	if len(errs) != 1 || !strings.Contains(errs[0], fdsCheckName) {
		t.Fatalf("want one error naming the reserved check, got %v", errs)
	}
}
