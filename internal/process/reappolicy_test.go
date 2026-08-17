package process

import (
	"strings"
	"testing"
)

func TestParseReapPolicyAbsentAuthorizesNothing(t *testing.T) {
	selector, warnings := ParseReapPolicy(map[string]any{})
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if selector.Configured() {
		t.Fatal("a service without a reap block must authorize nothing")
	}
}

func TestParseReapPolicyReadsPairedSelector(t *testing.T) {
	selector, warnings := ParseReapPolicy(map[string]any{
		SectionReap: map[string]any{
			ReapKeyKillOnlyIf: map[string]any{
				ReapKeyUsers:  []any{"root"},
				ReapKeyExeAny: []any{"/usr/bin/dbus-daemon"},
			},
		},
	})
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if !selector.Configured() {
		t.Fatal("a complete selector must be configured")
	}
	proc := Process{PID: 300, User: "root", UID: 0, Exe: "/usr/bin/dbus-daemon", ExeOK: true}
	if !selector.Killable(proc, fakeUsers(map[string]uint32{"root": 0})) {
		t.Fatal("the declared identity must be killable")
	}
}

// A half-written selector must authorize nothing rather than whatever half it
// carries: users-only would otherwise match on the UID alone.
func TestParseReapPolicyPartialSelectorAuthorizesNothing(t *testing.T) {
	selector, warnings := ParseReapPolicy(map[string]any{
		SectionReap: map[string]any{
			ReapKeyKillOnlyIf: map[string]any{ReapKeyUsers: []any{"root"}},
		},
	})
	if selector.Configured() || len(selector.Users) != 0 {
		t.Fatalf("partial selector must be dropped, got %+v", selector)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], ReapKeyExeAny) {
		t.Fatalf("warnings = %v, want one naming %s", warnings, ReapKeyExeAny)
	}
}

func TestParseReapPolicyRejectsUnknownKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		tree map[string]any
		want string
	}{
		{
			name: "unknown block key",
			tree: map[string]any{SectionReap: map[string]any{"kill_if": map[string]any{}}},
			want: "kill_if is not supported",
		},
		{
			name: "unknown selector key",
			tree: map[string]any{SectionReap: map[string]any{
				ReapKeyKillOnlyIf: map[string]any{
					ReapKeyUsers: []any{"root"}, ReapKeyExeAny: []any{"/usr/bin/x"}, "exe": "/usr/bin/x",
				},
			}},
			want: "exe is not supported",
		},
		{
			name: "block is not a mapping",
			tree: map[string]any{SectionReap: "root"},
			want: "must be a mapping",
		},
		{
			name: "selector is not a mapping",
			tree: map[string]any{SectionReap: map[string]any{ReapKeyKillOnlyIf: "root"}},
			want: "must be a mapping",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, warnings := ParseReapPolicy(tc.tree)
			if len(warnings) == 0 {
				t.Fatalf("want a warning naming %q", tc.want)
			}
			joined := strings.Join(warnings, "; ")
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("warnings = %q, want one containing %q", joined, tc.want)
			}
		})
	}
}

// The reap selector reuses stop_policy's gate, so every refusal that gate owns
// applies unchanged: a delegated process, an unresolvable exe and PID 1.
func TestReapSelectorKeepsKillableRefusals(t *testing.T) {
	selector, _ := ParseReapPolicy(map[string]any{
		SectionReap: map[string]any{
			ReapKeyKillOnlyIf: map[string]any{
				ReapKeyUsers:  []any{"root"},
				ReapKeyExeAny: []any{"/usr/bin/dbus-daemon"},
			},
		},
	})
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
