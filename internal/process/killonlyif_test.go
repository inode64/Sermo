package process

import (
	"strings"
	"testing"
)

const killOnlyIfTestPath = "then.kill.kill_only_if"

func TestParseKillOnlyIfReadsPairedSelector(t *testing.T) {
	selector, warnings := ParseKillOnlyIf(map[string]any{
		StopPolicyKeyUsers:  []any{"root"},
		StopPolicyKeyExeAny: []any{"/usr/bin/dbus-daemon"},
	}, killOnlyIfTestPath)
	if len(warnings) != 0 || !selector.Configured() {
		t.Fatalf("selector = %+v warnings = %v", selector, warnings)
	}
	proc := Process{PID: 300, User: "root", UID: 0, Exe: "/usr/bin/dbus-daemon", ExeOK: true}
	if !selector.Killable(proc, fakeUsers(map[string]uint32{"root": 0})) {
		t.Fatal("the declared identity must be killable")
	}
}

// A half-written selector must authorize nothing rather than whatever half it
// carries: users-only would otherwise match on the UID alone.
func TestParseKillOnlyIfPartialSelectorAuthorizesNothing(t *testing.T) {
	selector, warnings := ParseKillOnlyIf(map[string]any{StopPolicyKeyUsers: []any{"root"}}, killOnlyIfTestPath)
	if selector.Configured() || len(selector.Users) != 0 {
		t.Fatalf("partial selector must be dropped, got %+v", selector)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], StopPolicyKeyExeAny) {
		t.Fatalf("warnings = %v, want one naming %s", warnings, StopPolicyKeyExeAny)
	}
}

func TestParseKillOnlyIfRejectsMalformedSelectors(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  any
		want string
	}{
		{name: "unknown key", raw: map[string]any{StopPolicyKeyUsers: []any{"root"}, StopPolicyKeyExeAny: []any{"/usr/bin/x"}, "exe": "/usr/bin/x"}, want: "exe is not supported"},
		{name: "not a mapping", raw: "root", want: "must be a mapping"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, warnings := ParseKillOnlyIf(tc.raw, killOnlyIfTestPath)
			if joined := strings.Join(warnings, "; "); !strings.Contains(joined, killOnlyIfTestPath) || !strings.Contains(joined, tc.want) {
				t.Fatalf("warnings = %q, want one at %s containing %q", joined, killOnlyIfTestPath, tc.want)
			}
		})
	}
}

// The selector shares stop_policy's gate, so every refusal that gate owns
// applies unchanged: a delegated process, an unresolvable exe and PID 1.
func TestKillOnlyIfKeepsKillableRefusals(t *testing.T) {
	selector, _ := ParseKillOnlyIf(map[string]any{
		StopPolicyKeyUsers:  []any{"root"},
		StopPolicyKeyExeAny: []any{"/usr/bin/dbus-daemon"},
	}, killOnlyIfTestPath)
	resolve := fakeUsers(map[string]uint32{"root": 0})
	for _, tc := range []struct {
		name string
		proc Process
	}{
		{"delegated", Process{PID: 300, UID: 0, Exe: "/usr/bin/dbus-daemon", ExeOK: true, Delegated: true}},
		{"unresolvable exe", Process{PID: 300, UID: 0, Exe: "/usr/bin/dbus-daemon", ExeOK: false}},
		{"pid 1", Process{PID: 1, UID: 0, Exe: "/usr/bin/dbus-daemon", ExeOK: true}},
		{"other user", Process{PID: 300, UID: 65534, Exe: "/usr/bin/dbus-daemon", ExeOK: true}},
		{"other exe", Process{PID: 300, UID: 0, Exe: "/usr/bin/other", ExeOK: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if selector.Killable(tc.proc, resolve) {
				t.Fatalf("%s must never be killable", tc.name)
			}
		})
	}
}
