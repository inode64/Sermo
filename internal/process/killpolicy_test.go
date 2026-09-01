package process

import (
	"fmt"
	"strings"
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
	for _, value := range []any{"notaduration", "0s", "-1s", "", 10} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			tree := map[string]any{"stop_policy": map[string]any{"graceful_timeout": value}}
			policy, warnings := ParseStopPolicy(tree)
			if len(warnings) != 1 || !strings.Contains(warnings[0], "valid positive duration") {
				t.Fatalf("warnings = %v, want one positive-duration warning", warnings)
			}
			if policy.GracefulTimeout != 0 {
				t.Errorf("bad duration should yield 0, got %v", policy.GracefulTimeout)
			}
		})
	}
}

func TestParseStopPolicyOwnsSignalAuthorizationDiagnostics(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		tree map[string]any
		want string
	}{
		{
			name: "invalid force mode",
			tree: map[string]any{SectionStopPolicy: map[string]any{StopPolicyKeyForceKill: "eventually"}},
			want: `stop_policy.force_kill must be a boolean or "auto"`,
		},
		{
			name: "force without selector",
			tree: map[string]any{SectionStopPolicy: map[string]any{StopPolicyKeyForceKill: true}},
			want: "stop_policy.force_kill=true requires kill_only_if",
		},
		{
			name: "partial selector",
			tree: map[string]any{SectionStopPolicy: map[string]any{
				StopPolicyKeyKillOnlyIf: map[string]any{StopPolicyKeyUsers: []any{"root"}},
			}},
			want: "stop_policy.kill_only_if must define both users and exe_any",
		},
	} {
		_, warnings := ParseStopPolicy(test.tree)
		if !strings.Contains(strings.Join(warnings, "\n"), test.want) {
			t.Errorf("%s: warnings = %v, want %q", test.name, warnings, test.want)
		}
	}
}

// A daemon whose workload children re-exec the same binary as the daemon itself
// (GlusterFS bricks, for one) is separable only by cmdline. The identity derived
// from force_kill: auto has to inherit that narrowing, or stopping the daemon
// would authorize signalling the workload the init unit deliberately left running.
func TestEnableAutomaticReapingNarrowsPairsByCmd(t *testing.T) {
	const exe = "/opt/sermo-test/glusterfsd"
	daemon := []string{"/opt/sermo-test/glusterd", "-p", "/run/glusterd.pid"}
	brick := []string{
		"/opt/sermo-test/glusterfsd", "--xlator-option",
		"*-posix.glusterd-uuid=70d985fe", "--process-name", "brick",
	}

	got := EnableAutomaticReaping(KillPolicy{Automatic: true}, []Selector{
		{Name: "main", Type: SelectorCommandMatch, Exe: exe, Cmd: `^(\S*/)?glusterd(\s|$)`, User: "gluster"},
	})
	if !got.ForceKill {
		t.Fatalf("policy = %+v, want force kill enabled", got)
	}

	resolve := fakeUsers(map[string]uint32{"gluster": 120})
	if !got.KillOnlyIf.Killable(Process{PID: 10, UID: 120, Exe: exe, ExeOK: true, Cmdline: daemon}, resolve) {
		t.Error("the management daemon must stay killable")
	}
	if got.KillOnlyIf.Killable(Process{PID: 11, UID: 120, Exe: exe, ExeOK: true, Cmdline: brick}, resolve) {
		t.Error("a sibling sharing exe and user but not the cmd must never be killable")
	}
}

// A delegated process is the service's own workload, kept alive on purpose by an
// init unit that stops only its main process. It stays visible in monitoring and
// is never signalled — not even when it matches an explicit kill_only_if.
func TestKillSelectorNeverSignalsDelegated(t *testing.T) {
	resolve := fakeUsers(map[string]uint32{"mysql": 110})
	sel := KillSelector{Users: []string{"mysql"}, ExeAny: []string{testExe}}
	proc := Process{PID: 100, UID: 110, Exe: testExe, ExeOK: true}

	if !sel.Killable(proc, resolve) {
		t.Fatal("baseline process must be killable, otherwise this proves nothing")
	}
	proc.Delegated = true
	if sel.Killable(proc, resolve) {
		t.Error("a delegated process must never be killable")
	}
}

// Nor may a delegated selector contribute authority to force_kill: auto.
func TestEnableAutomaticReapingIgnoresDelegatedSelectors(t *testing.T) {
	got := EnableAutomaticReaping(KillPolicy{Automatic: true}, []Selector{
		{Name: "brick", Type: SelectorCommandMatch, Exe: testExe, User: "mysql", Delegated: true},
	})
	if got.ForceKill {
		t.Fatalf("policy = %+v, want force kill disabled: a delegated selector authorizes nothing", got)
	}
}
