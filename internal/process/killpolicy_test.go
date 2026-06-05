package process

import (
	"testing"
	"time"
)

const testExe = "/opt/sermo-test/mysqld"

func TestKillSelectorKillable(t *testing.T) {
	resolve := fakeUsers(map[string]uint32{"mysql": 110, "www-data": 33})
	sel := KillSelector{Users: []string{"mysql"}, ExeAny: []string{testExe}}

	cases := []struct {
		name string
		proc Process
		want bool
	}{
		{"exe and user match", Process{UID: 110, Exe: testExe, ExeOK: true}, true},
		{"wrong exe", Process{UID: 110, Exe: "/opt/sermo-test/other", ExeOK: true}, false},
		{"wrong user", Process{UID: 999, Exe: testExe, ExeOK: true}, false},
		{"unresolvable exe", Process{UID: 110, Exe: "", ExeOK: false}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sel.Killable(tc.proc, resolve); got != tc.want {
				t.Errorf("Killable = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestKillSelectorEmptyMatchesNothing(t *testing.T) {
	resolve := fakeUsers(map[string]uint32{"mysql": 110})
	p := Process{UID: 110, Exe: testExe, ExeOK: true}

	if (KillSelector{ExeAny: []string{testExe}}).Killable(p, resolve) {
		t.Error("selector with no users must not be killable")
	}
	if (KillSelector{Users: []string{"mysql"}}).Killable(p, resolve) {
		t.Error("selector with no exe_any must not be killable")
	}
	if (KillSelector{}).Killable(p, resolve) {
		t.Error("empty selector must not be killable")
	}
}

func TestParseStopPolicy(t *testing.T) {
	tree := map[string]any{
		"stop_policy": map[string]any{
			"graceful_timeout": "30s",
			"term_timeout":     "15s",
			"kill_timeout":     "5s",
			"force_kill":       true,
			"kill_only_if": map[string]any{
				"users":   []any{"mysql"},
				"exe_any": []any{"/usr/sbin/mysqld", "/usr/bin/mariadbd"},
			},
		},
	}
	policy, warnings := ParseStopPolicy(tree)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if policy.GracefulTimeout != 30*time.Second || policy.TermTimeout != 15*time.Second || policy.KillTimeout != 5*time.Second {
		t.Errorf("timeouts = %v/%v/%v", policy.GracefulTimeout, policy.TermTimeout, policy.KillTimeout)
	}
	if !policy.ForceKill {
		t.Error("force_kill not parsed")
	}
	if len(policy.KillOnlyIf.Users) != 1 || len(policy.KillOnlyIf.ExeAny) != 2 {
		t.Errorf("kill_only_if = %+v", policy.KillOnlyIf)
	}
}

func TestParseStopPolicyBadDurationWarns(t *testing.T) {
	tree := map[string]any{"stop_policy": map[string]any{"graceful_timeout": "notaduration"}}
	policy, warnings := ParseStopPolicy(tree)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want 1", warnings)
	}
	if policy.GracefulTimeout != 0 {
		t.Errorf("bad duration should yield 0, got %v", policy.GracefulTimeout)
	}
}
