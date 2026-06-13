package config

import "testing"

func TestValidateFirewallRulesCheck(t *testing.T) {
	good := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  fw: { type: firewall_rules, backend: nftables, min_rules: 1 }
`)
	if hasIssue(good, "fw") {
		t.Fatalf("valid firewall_rules check flagged: %v", good)
	}

	bad := validateService(t, `
kind: service
name: svc
service: { name: x }
policy: { cooldown: 5m }
checks:
  backend: { type: firewall_rules, backend: pf }
  rules:   { type: firewall_rules, min_rules: 0 }
`)
	mustHave(t, bad, "checks.backend.backend must be auto, nftables or iptables")
	mustHave(t, bad, "checks.rules.min_rules must be a positive integer")
}
