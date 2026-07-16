package process

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"sermo/internal/execx"
)

type fakeGetentRunner struct {
	outputs map[string]string
	calls   map[string]int
}

func (f *fakeGetentRunner) Run(_ context.Context, name string, args ...string) (execx.Result, error) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	key := strings.Join(append([]string{name}, args...), "\x00")
	f.calls[key]++
	if out, ok := f.outputs[key]; ok {
		return execx.Result{Stdout: out}, nil
	}
	return execx.Result{ExitCode: 2}, errors.New("not found")
}

func TestUserLookupGetentResolvesAndCaches(t *testing.T) {
	runner := &fakeGetentRunner{outputs: map[string]string{
		"getent\x00passwd\x00ldap-user":  "ldap-user:x:4242:4243:LDAP User:/home/ldap-user:/bin/bash\n",
		"getent\x00passwd\x004242":       "ldap-user:x:4242:4243:LDAP User:/home/ldap-user:/bin/bash\n",
		"getent\x00group\x00ldap-group":  "ldap-group:x:4243:ldap-user\n",
		"getent\x00group\x004243":        "ldap-group:x:4243:ldap-user\n",
		"getent\x00passwd\x00bad-format": "bad-format:x:not-a-number\n",
	}}
	lookup := NewUserLookup(UserLookupConfig{Mode: UserLookupGetent, Timeout: time.Second, Runner: runner})

	uid, ok := lookup.ResolveUser("ldap-user")
	if !ok || uid != 4242 {
		t.Fatalf("ResolveUser = %d/%v, want 4242/true", uid, ok)
	}
	uid, ok = lookup.ResolveUser("ldap-user")
	if !ok || uid != 4242 {
		t.Fatalf("cached ResolveUser = %d/%v, want 4242/true", uid, ok)
	}
	if got := runner.calls["getent\x00passwd\x00ldap-user"]; got != 1 {
		t.Fatalf("getent passwd ldap-user calls = %d, want 1", got)
	}

	if got := lookup.Username(4242); got != "ldap-user" {
		t.Fatalf("Username = %q, want ldap-user", got)
	}
	gid, ok := lookup.ResolveGroup("ldap-group")
	if !ok || gid != 4243 {
		t.Fatalf("ResolveGroup = %d/%v, want 4243/true", gid, ok)
	}
	if got := lookup.GroupName(4243); got != "ldap-group" {
		t.Fatalf("GroupName = %q, want ldap-group", got)
	}
	if _, ok := lookup.ResolveUser("bad-format"); ok {
		t.Fatal("bad getent passwd line must not resolve")
	}
}

func TestUserLookupNumericMode(t *testing.T) {
	runner := &fakeGetentRunner{outputs: map[string]string{}}
	lookup := NewUserLookup(UserLookupConfig{Mode: UserLookupNumeric, Timeout: time.Second, Runner: runner})

	uid, ok := lookup.ResolveUser("1001")
	if !ok || uid != 1001 {
		t.Fatalf("numeric ResolveUser = %d/%v, want 1001/true", uid, ok)
	}
	if _, ok := lookup.ResolveUser("ldap-user"); ok {
		t.Fatal("name lookup in numeric mode must fail closed")
	}
	if got := lookup.Username(1001); got != "" {
		t.Fatalf("Username in numeric mode = %q, want empty", got)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("numeric mode ran commands: %v", runner.calls)
	}
}

func TestUserLookupLookupPolicies(t *testing.T) {
	autoID, autoName := uint32(0), ""
	autoOK := false
	if !cgoEnabled {
		autoID, autoName, autoOK = 22, "getent", true
	}
	tests := []struct {
		name                   string
		mode                   string
		nativeOK, getentOK     bool
		wantID                 uint32
		wantName               string
		wantOK                 bool
		wantNative, wantGetent int
	}{
		{name: "numeric fails closed", mode: UserLookupNumeric},
		{name: "native", mode: UserLookupNative, nativeOK: true, wantID: 11, wantName: "native", wantOK: true, wantNative: 1},
		{name: "getent preferred", mode: UserLookupGetent, nativeOK: true, getentOK: true, wantID: 22, wantName: "getent", wantOK: true, wantGetent: 1},
		{name: "getent falls back to native", mode: UserLookupGetent, nativeOK: true, wantID: 11, wantName: "native", wantOK: true, wantNative: 1, wantGetent: 1},
		{name: "auto uses native", mode: UserLookupAuto, nativeOK: true, getentOK: true, wantID: 11, wantName: "native", wantOK: true, wantNative: 1},
		{name: "auto fallback depends on cgo", mode: UserLookupAuto, getentOK: true, wantID: autoID, wantName: autoName, wantOK: autoOK, wantNative: 1, wantGetent: boolToInt(!cgoEnabled)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lookup := NewUserLookup(UserLookupConfig{Mode: tc.mode})
			var nativeIDCalls, getentIDCalls, nativeNameCalls, getentNameCalls int
			nativeID := func(string) (uint32, bool) {
				nativeIDCalls++
				return 11, tc.nativeOK
			}
			getentID := func(string) (uint32, bool) {
				getentIDCalls++
				return 22, tc.getentOK
			}
			if id, ok := lookup.lookupID("target", nativeID, getentID); id != tc.wantID || ok != tc.wantOK {
				t.Fatalf("lookupID = %d/%v, want %d/%v", id, ok, tc.wantID, tc.wantOK)
			}
			if nativeIDCalls != tc.wantNative || getentIDCalls != tc.wantGetent {
				t.Fatalf("lookupID calls = native:%d getent:%d, want native:%d getent:%d", nativeIDCalls, getentIDCalls, tc.wantNative, tc.wantGetent)
			}

			nativeName := func(uint32) (string, bool) {
				nativeNameCalls++
				return "native", tc.nativeOK
			}
			getentName := func(uint32) (string, bool) {
				getentNameCalls++
				return "getent", tc.getentOK
			}
			if name, ok := lookup.lookupName(11, nativeName, getentName); name != tc.wantName || ok != tc.wantOK {
				t.Fatalf("lookupName = %q/%v, want %q/%v", name, ok, tc.wantName, tc.wantOK)
			}
			if nativeNameCalls != tc.wantNative || getentNameCalls != tc.wantGetent {
				t.Fatalf("lookupName calls = native:%d getent:%d, want native:%d getent:%d", nativeNameCalls, getentNameCalls, tc.wantNative, tc.wantGetent)
			}
		})
	}
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
