package config

import (
	"maps"
	"strings"
	"testing"

	"sermo/internal/checks"
	"sermo/internal/dockerctl"
	"sermo/internal/rules"
)

func straysTree(extra map[string]any) map[string]any {
	tree := map[string]any{
		keyName: "acpid",
		"processes": map[string]any{
			"main": map[string]any{"exe": "/usr/bin/acpid", "user": "root"},
		},
	}
	maps.Copy(tree, extra)
	return tree
}

func injectedStraysCheck(t *testing.T, tree map[string]any) (map[string]any, bool) {
	t.Helper()
	if errs := expandStrays(tree); len(errs) > 0 {
		t.Fatalf("expandStrays: %v", errs)
	}
	checksMap, _ := tree[sectionChecks].(map[string]any)
	entry, ok := checksMap[straysCheckName].(map[string]any)
	return entry, ok
}

func TestStraysInjectedAsAVerdictlessSensor(t *testing.T) {
	tree := straysTree(nil)
	entry, ok := injectedStraysCheck(t, tree)
	if !ok {
		t.Fatalf("strays check not injected: %v", tree[sectionChecks])
	}
	if entry[checks.CheckKeyType] != checks.CheckTypeStrays {
		t.Fatalf("type = %v, want %q", entry[checks.CheckKeyType], checks.CheckTypeStrays)
	}
	// A stray must not make the service read as unhealthy or dent its SLA: the
	// daemon is serving, something merely accumulated beside it.
	if entry[checks.CheckKeyReports] != checks.ReportsState {
		t.Fatalf("reports = %v, want %q", entry[checks.CheckKeyReports], checks.ReportsState)
	}
}

// No rule, deliberately: on real hosts the raw condition is dominated by
// workloads a profile marked `delegated: true`, so a fleet-wide alert would
// mostly report incomplete catalog coverage. Alerting stays the operator's call.
func TestStraysInjectsNoRule(t *testing.T) {
	tree := straysTree(nil)
	if _, ok := injectedStraysCheck(t, tree); !ok {
		t.Fatal("strays check not injected")
	}
	if ruleMap, present := tree[rules.SectionRules].(map[string]any); present && len(ruleMap) > 0 {
		t.Fatalf("strays must inject no rule, got %v", ruleMap)
	}
}

// Without selectors there is nothing to contrast the control group against, so
// the sensor would report a permanent, meaningless verdict.
func TestStraysSkippedWithoutSelectors(t *testing.T) {
	tree := map[string]any{keyName: "acpid"}
	if _, ok := injectedStraysCheck(t, tree); ok {
		t.Fatal("a service declaring no processes must get no strays check")
	}
}

// An external control backend attributes a container's or domain's whole PID set
// to the service, where a process no selector names is ordinary rather than a
// leftover.
func TestStraysSkippedForExternalControlBackends(t *testing.T) {
	tree := straysTree(map[string]any{
		SectionControl: map[string]any{
			keyType:                       dockerctl.ControlType,
			dockerctl.ControlKeyContainer: "app",
		},
	})
	if _, ok := injectedStraysCheck(t, tree); ok {
		t.Fatal("a docker-controlled service must get no strays check")
	}
}

// The injected name is reserved, and the refusal has to say which sugar claims
// it: the operator never asked for this entry.
func TestStraysRefusesToShadowAnOperatorCheck(t *testing.T) {
	tree := straysTree(map[string]any{
		sectionChecks: map[string]any{
			straysCheckName: map[string]any{checks.CheckKeyType: checks.CheckTypeCommand},
		},
	})
	errs := expandStrays(tree)
	if len(errs) != 1 {
		t.Fatalf("want exactly one error, got %v", errs)
	}
	if !strings.Contains(errs[0], checks.CheckTypeStrays) || !strings.Contains(errs[0], straysCheckName) {
		t.Fatalf("error %q must name the sugar and the check", errs[0])
	}
}
