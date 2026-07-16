package config

import "testing"

func TestValidateFirewallRulesCheck(t *testing.T) {
	assertServiceValidation(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  fw: { type: firewall_rules, backend: nftables, min_rules: 1 }
`, "fw", `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  backend: { type: firewall_rules, backend: pf }
  rules:   { type: firewall_rules, min_rules: 0 }
`,
		"checks.backend.backend must be auto, nftables or iptables",
		"checks.rules.min_rules must be a positive integer")
}
