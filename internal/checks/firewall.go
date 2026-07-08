package checks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/execx"
	"sermo/internal/output"
)

// Firewall-backend selectors for the `firewall_rules` check's `backend:` field.
// Exported so config validation checks the same vocabulary this owner enforces.
const (
	FirewallBackendAuto     = "auto"
	FirewallBackendNftables = "nftables"
	FirewallBackendIptables = "iptables"
	// FirewallBackendNftAlias is the accepted shorthand for the nftables backend.
	FirewallBackendNftAlias = "nft"
	// FirewallBackendSummary is the user-facing list of firewall backend selectors.
	FirewallBackendSummary = FirewallBackendAuto + ", " + FirewallBackendNftables + " or " + FirewallBackendIptables
)

const (
	iptablesSaveIPv4         = "iptables-save"
	iptablesSaveIPv6         = "ip6tables-save"
	iptablesSaveCommandCount = 2
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
		DataKeyBackend:  sample.Backend,
		DataKeyRules:    sample.Rules,
		DataKeyMinRules: c.minRules,
		DataKeyValue:    sample.Rules,
	}
	return res
}

func buildFirewallRulesCheck(b base, entry map[string]any, runner execx.Runner, deps Deps) (Check, string) {
	backend := cfgval.AsString(entry[CheckKeyBackend])
	if backend == "" {
		backend = FirewallBackendAuto
	}
	if backend == FirewallBackendNftAlias {
		backend = FirewallBackendNftables
	}
	if !validFirewallBackend(backend) {
		return nil, "firewall_rules check backend must be " + FirewallBackendSummary
	}
	minRules := uint64(1)
	if v, present := entry[CheckKeyMinRules]; present {
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
	case FirewallBackendAuto, FirewallBackendNftables, FirewallBackendIptables:
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
	case FirewallBackendAuto:
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
	case FirewallBackendNftables:
		return sampleNftablesRules(ctx, runner)
	case FirewallBackendIptables:
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
	return FirewallRulesSample{Backend: FirewallBackendNftables, Rules: rules}, nil
}

func sampleIptablesRules(ctx context.Context, runner execx.Runner) (FirewallRulesSample, error) {
	var rules uint64
	var errs []error
	for _, command := range [...]string{iptablesSaveIPv4, iptablesSaveIPv6} {
		res, err := runner.Run(ctx, command)
		if err != nil || res.ExitCode != execx.ExitCodeSuccess {
			errs = append(errs, commandResultError(command, res, err))
			continue
		}
		rules += countIptablesRules(res.Stdout)
	}
	if len(errs) == iptablesSaveCommandCount {
		return FirewallRulesSample{}, joinFirewallErrors(errs)
	}
	return FirewallRulesSample{Backend: FirewallBackendIptables, Rules: rules}, nil
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
	if res.ExitCode == execx.ExitCodeRunFailure {
		msg := execx.OperatorFailure(err, res, execx.NoTimeout)
		if msg == "" {
			msg = execx.CommandDidNotStart
		}
		return fmt.Errorf("%s: %s", command, msg)
	}
	msg := output.FirstNonEmptyLine(res.Stderr)
	if msg == "" {
		msg = output.FirstNonEmptyLine(res.Stdout)
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
	for _, line := range strings.Split(out, checkLineSeparator) {
		if strings.HasPrefix(strings.TrimSpace(line), "-A ") {
			rules++
		}
	}
	return rules
}
