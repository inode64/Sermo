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
		{"exe and user match", Process{PID: 100, UID: 110, Exe: testExe, ExeOK: true}, true},
		{"wrong exe", Process{PID: 100, UID: 110, Exe: "/opt/sermo-test/other", ExeOK: true}, false},
		{"wrong user", Process{PID: 100, UID: 999, Exe: testExe, ExeOK: true}, false},
		{"unresolvable exe", Process{PID: 100, UID: 110, Exe: "", ExeOK: false}, false},
		{"pid 1 protected", Process{PID: 1, UID: 110, Exe: testExe, ExeOK: true}, false},
		{"kernel thread protected", Process{PID: 22, PPID: 2, UID: 0, Exe: "", ExeOK: false}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sel.Killable(tc.proc, resolve); got != tc.want {
				t.Errorf("Killable = %v, want %v", got, tc.want)
			}
		})
	}

	// Multi-entry lists act as OR within users and within exe_any (AND across
	// the two categories). Also exercises canonicalize fallback to Clean when
	// EvalSymlinks cannot run in test (nonexistent path).
	multi := KillSelector{
		Users:  []string{"nope", "mysql"},
		ExeAny: []string{"/no", "/opt//sermo-test/./mysqld"},
	}
	if !multi.Killable(Process{PID: 100, UID: 110, Exe: testExe, ExeOK: true}, resolve) {
		t.Error("multi selector should match on second user+exe (canonicalized)")
	}
	if multi.Killable(Process{PID: 100, UID: 110, Exe: "/different", ExeOK: true}, resolve) {
		t.Error("multi selector must not match when exe misses all exe_any")
	}
}

func TestKillSelectorEmptyMatchesNothing(t *testing.T) {
	resolve := fakeUsers(map[string]uint32{"mysql": 110})
	p := Process{PID: 100, UID: 110, Exe: testExe, ExeOK: true}

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

func TestEnableAutomaticReapingUsesStrictSelectorPairs(t *testing.T) {
	policy, warnings := ParseStopPolicy(map[string]any{
		"stop_policy": map[string]any{"force_kill": StopPolicyForceKillAuto},
	})
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	got := EnableAutomaticReaping(policy, []Selector{
		{Name: "main", Type: SelectorCommandMatch, Exe: "/usr/sbin/alpha", User: "alpha"},
		{Name: "worker", Type: SelectorCommandMatch, Exe: "/usr/sbin/beta", User: "beta"},
		{Name: "pidfile", Type: SelectorPidfile, Paths: []string{"/run/alpha.pid"}},
		{Name: "incomplete", Type: SelectorCommandMatch, Exe: "/usr/sbin/incomplete"},
	})
	if !got.Automatic || !got.ForceKill {
		t.Fatalf("policy = %+v, want enabled automatic reaping", got)
	}
	resolve := fakeUsers(map[string]uint32{"alpha": 101, "beta": 102})
	for _, tc := range []struct {
		name string
		proc Process
		want bool
	}{
		{"first pair", Process{PID: 10, UID: 101, Exe: "/usr/sbin/alpha", ExeOK: true}, true},
		{"second pair", Process{PID: 11, UID: 102, Exe: "/usr/sbin/beta", ExeOK: true}, true},
		{"crossed identity", Process{PID: 12, UID: 101, Exe: "/usr/sbin/beta", ExeOK: true}, false},
		{"unresolved executable", Process{PID: 13, UID: 101, Exe: "/usr/sbin/alpha", ExeOK: false}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if actual := got.KillOnlyIf.Killable(tc.proc, resolve); actual != tc.want {
				t.Errorf("Killable = %v, want %v", actual, tc.want)
			}
		})
	}
}

func TestEnableAutomaticReapingWithoutStrictSelectorStaysDisabled(t *testing.T) {
	policy := EnableAutomaticReaping(KillPolicy{Automatic: true}, []Selector{
		{Name: "pidfile", Type: SelectorPidfile, Paths: []string{"/run/svc.pid"}},
		{Name: "cmd", Type: SelectorCommandMatch, Cmd: "svc"},
	})
	if policy.ForceKill {
		t.Fatalf("policy = %+v, want force kill disabled without exact exe/user", policy)
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
