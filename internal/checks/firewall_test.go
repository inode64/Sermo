package checks

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"sermo/internal/execx"
)

func TestCountLoadedNftablesRulesHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := countLoadedNftablesRules(ctx)
	if err == nil {
		t.Fatal("cancelled context must fail")
	}
	if !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("error = %q, want cancelled", err.Error())
	}
}

func TestCountIptablesRules(t *testing.T) {
	out := strings.Join([]string{
		"*filter",
		":INPUT ACCEPT [0:0]",
		"-P FORWARD DROP",
		"-A INPUT -m conntrack --ctstate ESTABLISHED -j ACCEPT",
		" -A INPUT -p tcp --dport 22 -j ACCEPT",
		"COMMIT",
	}, "\n")
	if got := countIptablesRules(out); got != 2 {
		t.Fatalf("countIptablesRules() = %d, want 2", got)
	}
}

func TestFirewallRulesCheckRun(t *testing.T) {
	tests := []struct {
		name    string
		sample  FirewallRulesSample
		err     error
		wantOK  bool
		wantMsg string
	}{
		{
			name:    "enough rules",
			sample:  FirewallRulesSample{Backend: firewallBackendNftables, Rules: 2},
			wantOK:  true,
			wantMsg: "has 2 rules",
		},
		{
			name:    "no rules",
			sample:  FirewallRulesSample{Backend: firewallBackendNftables, Rules: 0},
			wantOK:  false,
			wantMsg: "below min 1",
		},
		{
			name:    "sampler error",
			err:     fmt.Errorf("nft failed"),
			wantOK:  false,
			wantMsg: "nft failed",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := firewallRulesCheck{
				base:     base{name: "fw", timeout: time.Second},
				backend:  firewallBackendAuto,
				minRules: 1,
				sampler: func(context.Context, string, execx.Runner) (FirewallRulesSample, error) {
					return tc.sample, tc.err
				},
			}
			res := c.Run(context.Background())
			if res.OK != tc.wantOK {
				t.Fatalf("OK = %v, want %v (%+v)", res.OK, tc.wantOK, res)
			}
			if !strings.Contains(res.Message, tc.wantMsg) {
				t.Fatalf("message = %q, want substring %q", res.Message, tc.wantMsg)
			}
			if tc.err == nil && res.Data["rules"] != tc.sample.Rules {
				t.Fatalf("data rules = %v, want %d", res.Data["rules"], tc.sample.Rules)
			}
		})
	}
}

func TestDefaultFirewallRulesSampler(t *testing.T) {
	tests := []struct {
		name    string
		backend string
		nft     func(context.Context) (uint64, error)
		runner  firewallRunner
		want    FirewallRulesSample
		wantErr string
	}{
		{
			name:    "auto prefers nftables when rules exist",
			backend: firewallBackendAuto,
			nft:     func(context.Context) (uint64, error) { return 1, nil },
			want:    FirewallRulesSample{Backend: firewallBackendNftables, Rules: 1},
		},
		{
			name:    "auto falls back to iptables when nftables has no rules",
			backend: firewallBackendAuto,
			nft:     func(context.Context) (uint64, error) { return 0, nil },
			runner: firewallRunner{
				"iptables-save":  {result: execx.Result{Stdout: "-A INPUT -j ACCEPT\n-A OUTPUT -j ACCEPT\n"}},
				"ip6tables-save": {result: execx.Result{Stdout: "-A INPUT -j ACCEPT\n"}},
			},
			want: FirewallRulesSample{Backend: firewallBackendIptables, Rules: 3},
		},
		{
			name:    "auto returns nftables zero when legacy tools are unavailable",
			backend: firewallBackendAuto,
			nft:     func(context.Context) (uint64, error) { return 0, nil },
			want:    FirewallRulesSample{Backend: firewallBackendNftables, Rules: 0},
		},
		{
			name:    "explicit nftables netlink error",
			backend: firewallBackendNftables,
			nft:     func(context.Context) (uint64, error) { return 0, fmt.Errorf("permission denied") },
			wantErr: "nftables: permission denied",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.nft == nil {
				tc.nft = func(context.Context) (uint64, error) { return 0, nil }
			}
			prev := nftablesRuleCounter
			nftablesRuleCounter = tc.nft
			defer func() { nftablesRuleCounter = prev }()

			got, err := defaultFirewallRulesSampler(context.Background(), tc.backend, tc.runner)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("defaultFirewallRulesSampler() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("sample = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestBuildFirewallRulesCheck(t *testing.T) {
	built, warns := Build(map[string]any{
		"fw": map[string]any{"type": "firewall_rules", "backend": "nft", "min_rules": 2},
	}, Deps{FirewallRulesSampler: func(context.Context, string, execx.Runner) (FirewallRulesSample, error) {
		return FirewallRulesSample{Backend: firewallBackendNftables, Rules: 2}, nil
	}})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(built) != 1 || !built[0].Check.Run(context.Background()).OK {
		t.Fatal("firewall_rules check should build and pass")
	}

	if _, warns := Build(map[string]any{"fw": map[string]any{"type": "firewall_rules", "backend": "pf"}}, Deps{}); len(warns) == 0 {
		t.Fatal("invalid firewall backend should warn")
	}
	if _, warns := Build(map[string]any{"fw": map[string]any{"type": "firewall_rules", "min_rules": 0}}, Deps{}); len(warns) == 0 {
		t.Fatal("invalid min_rules should warn")
	}
	// min_rules=1 is the lowest valid value (the guard rejects n < 1, not n <= 1).
	if _, warns := Build(map[string]any{
		"fw": map[string]any{"type": "firewall_rules", "backend": "nft", "min_rules": 1},
	}, Deps{FirewallRulesSampler: func(context.Context, string, execx.Runner) (FirewallRulesSample, error) {
		return FirewallRulesSample{Backend: firewallBackendNftables, Rules: 1}, nil
	}}); len(warns) != 0 {
		t.Fatalf("min_rules=1 must be valid, got warnings: %v", warns)
	}
}

type firewallRun struct {
	result execx.Result
	err    error
}

func TestFirewallRulesCheckCanceledSampler(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := firewallRulesCheck{
		base:    base{name: "fw", timeout: time.Millisecond},
		backend: firewallBackendNftables,
		sampler: func(context.Context, string, execx.Runner) (FirewallRulesSample, error) {
			return FirewallRulesSample{}, context.Canceled
		},
	}
	res := c.Run(ctx)
	if res.OK {
		t.Fatal("canceled firewall sampler must fail")
	}
	if !strings.Contains(res.Message, "firewall: cancelled") {
		t.Fatalf("message = %q, want firewall: cancelled", res.Message)
	}
}

type firewallRunner map[string]firewallRun

func (r firewallRunner) Run(_ context.Context, name string, args ...string) (execx.Result, error) {
	key := strings.TrimSpace(name + " " + strings.Join(args, " "))
	if run, ok := r[key]; ok {
		return run.result, run.err
	}
	return execx.Result{ExitCode: -1}, fmt.Errorf("%s unavailable", key)
}
