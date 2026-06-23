package checks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/execx"
)

const (
	firewallBackendAuto     = "auto"
	firewallBackendNftables = "nftables"
	firewallBackendIptables = "iptables"
)

// FirewallRulesSample is one observation of loaded packet-filter rules.
type FirewallRulesSample struct {
	Backend string
	Rules   uint64
}

// FirewallRulesSamplerFunc reads loaded nftables/iptables rules. The backend is
// auto, nftables or iptables. Injected for tests; the default reads nftables via
// netlink and iptables through iptables-save on the configured execx runner.
type FirewallRulesSamplerFunc func(ctx context.Context, backend string, runner execx.Runner) (FirewallRulesSample, error)

// firewallRulesCheck verifies that a packet-filter ruleset is actually loaded.
// It is health-style: OK==true means enough rules exist, so a host watch over it
// fires hooks when the firewall service is active but no rules are present.
type firewallRulesCheck struct {
	base
	backend  string
	minRules uint64
	runner   execx.Runner
	sampler  FirewallRulesSamplerFunc
}

func (c firewallRulesCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	sampler := c.sampler
	if sampler == nil {
		sampler = defaultFirewallRulesSampler
	}
	sample, err := sampler(ctx, c.backend, c.runner)
	if err != nil {
		return c.result(false, "firewall: "+execx.FormatContextOrError(err, c.timeout), start)
	}
	ok := sample.Rules >= c.minRules
	msg := fmt.Sprintf("firewall %s has %d rules (min %d)", sample.Backend, sample.Rules, c.minRules)
	if !ok {
		msg = fmt.Sprintf("firewall %s has %d rules, below min %d", sample.Backend, sample.Rules, c.minRules)
	}
	res := c.result(ok, msg, start)
	res.Data = map[string]any{
		"backend":   sample.Backend,
		"rules":     sample.Rules,
		"min_rules": c.minRules,
		"value":     sample.Rules,
	}
	return res
}

func buildFirewallRulesCheck(b base, entry map[string]any, runner execx.Runner, deps Deps) (Check, string) {
	backend := cfgval.AsString(entry["backend"])
	if backend == "" {
		backend = firewallBackendAuto
	}
	if backend == "nft" {
		backend = firewallBackendNftables
	}
	if !validFirewallBackend(backend) {
		return nil, "firewall_rules check backend must be auto, nftables or iptables"
	}
	minRules := uint64(1)
	if v, present := entry["min_rules"]; present {
		n, ok := cfgval.Int(v)
		if !ok || n < 1 {
			return nil, "firewall_rules check min_rules must be a positive integer"
		}
		minRules = uint64(n)
	}
	return firewallRulesCheck{
		base:     b,
		backend:  backend,
		minRules: minRules,
		runner:   runner,
		sampler:  deps.FirewallRulesSampler,
	}, ""
}

func validFirewallBackend(backend string) bool {
	switch backend {
	case firewallBackendAuto, firewallBackendNftables, firewallBackendIptables:
		return true
	default:
		return false
	}
}

// DefaultFirewallRulesSampler reads loaded nftables/iptables rules.
var DefaultFirewallRulesSampler FirewallRulesSamplerFunc = defaultFirewallRulesSampler

func defaultFirewallRulesSampler(ctx context.Context, backend string, runner execx.Runner) (FirewallRulesSample, error) {
	if runner == nil {
		runner = execx.CommandRunner{}
	}
	switch backend {
	case firewallBackendAuto:
		var errs []error
		nft, err := sampleNftablesRules(ctx, runner)
		nftOK := err == nil
		if err == nil && nft.Rules > 0 {
			return nft, nil
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("nftables: %w", err))
		}
		iptables, err := sampleIptablesRules(ctx, runner)
		if err == nil {
			return iptables, nil
		}
		errs = append(errs, fmt.Errorf("iptables: %w", err))
		if nftOK {
			return nft, nil
		}
		return FirewallRulesSample{}, joinFirewallErrors(errs)
	case firewallBackendNftables:
		return sampleNftablesRules(ctx, runner)
	case firewallBackendIptables:
		return sampleIptablesRules(ctx, runner)
	default:
		return FirewallRulesSample{}, fmt.Errorf("unknown backend %q", backend)
	}
}

func sampleNftablesRules(ctx context.Context, _ execx.Runner) (FirewallRulesSample, error) {
	rules, err := nftablesRuleCounter(ctx)
	if err != nil {
		return FirewallRulesSample{}, fmt.Errorf("nftables: %w", err)
	}
	return FirewallRulesSample{Backend: firewallBackendNftables, Rules: rules}, nil
}

func sampleIptablesRules(ctx context.Context, runner execx.Runner) (FirewallRulesSample, error) {
	var rules uint64
	var errs []error
	for _, command := range []string{"iptables-save", "ip6tables-save"} {
		res, err := runner.Run(ctx, command)
		if err != nil || res.ExitCode != 0 {
			errs = append(errs, commandResultError(command, res, err))
			continue
		}
		rules += countIptablesRules(res.Stdout)
	}
	if len(errs) == 2 {
		return FirewallRulesSample{}, joinFirewallErrors(errs)
	}
	return FirewallRulesSample{Backend: firewallBackendIptables, Rules: rules}, nil
}

func joinFirewallErrors(errs []error) error {
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			parts = append(parts, err.Error())
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return errors.New(strings.Join(parts, "; "))
}

func commandResultError(command string, res execx.Result, err error) error {
	if res.ExitCode == -1 {
		msg := execx.OperatorFailure(err, res, 0)
		if msg == "" {
			msg = "command failed to run"
		}
		return fmt.Errorf("%s: %s", command, msg)
	}
	msg := FirstNonEmptyLine(res.Stderr)
	if msg == "" {
		msg = FirstNonEmptyLine(res.Stdout)
	}
	if msg != "" {
		return fmt.Errorf("%s: %s", command, msg)
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%s exit %d", command, res.ExitCode)
}

func countIptablesRules(out string) uint64 {
	var rules uint64
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "-A ") {
			rules++
		}
	}
	return rules
}
