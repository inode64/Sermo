package config

import (
	"fmt"
	"testing"

	"sermo/internal/cfgval"
	"sermo/internal/rules"
)

// TestCatalogNFSRenewsMountdUnderOpenRC pins the only renewal OpenRC's mountd
// has: the nfs service runs it with no unit of its own, and a mountd left on the
// binary an nfs-utils upgrade replaced kept running for weeks because nothing
// restarted the service. Under systemd mountd is a unit of its own that the
// rpc-mountd profile renews, so the nfs profile must not restart for it there.
func TestCatalogNFSRenewsMountdUnderOpenRC(t *testing.T) {
	t.Parallel()
	const name = "restart-if-mountd-stopped-or-replaced"

	openrc := resolveCatalogService(t, "nfs", backendOpenRC)
	check := nested(t, openrc.Tree, sectionChecks, name)
	if got := cfgval.String(check["type"]); got != "process" {
		t.Fatalf("check type = %q, want process (check=%v)", got, check)
	}
	rule := nested(t, openrc.Tree, rules.SectionRules, name)
	if got := cfgval.String(nested(t, rule, rules.RuleFieldThen)[rules.RuleFieldAction]); got != "restart" {
		t.Fatalf("rule action = %q, want restart (rule=%v)", got, rule)
	}
	if got := cfgval.String(nested(t, rule, "if", "failed")["check"]); got != name {
		t.Fatalf("rule condition check = %q, want %q (rule=%v)", got, name, rule)
	}
	if got := fmt.Sprint(nested(t, rule, "for")["cycles"]); got != "2" {
		t.Fatalf("rule for.cycles = %q, want 2 (rule=%v)", got, rule)
	}

	systemd := resolveCatalogService(t, "nfs", "systemd")
	if checks, _ := systemd.Tree[sectionChecks].(map[string]any); checks[name] != nil {
		t.Fatalf("systemd must not carry the OpenRC mountd check: %v", checks[name])
	}
	if rulesMap, _ := systemd.Tree[rules.SectionRules].(map[string]any); rulesMap[name] != nil {
		t.Fatalf("systemd must not restart nfs for mountd: %v", rulesMap[name])
	}
}
