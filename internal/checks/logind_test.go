package checks

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLogindNamesCheckRun(t *testing.T) {
	for _, tc := range []struct {
		name            string
		noun, dataKey   string
		names           []string
		err             error
		wantOK          bool
		wantUnavailable bool
		wantMsg         string
		wantData        map[string]any
	}{
		{
			name: "no seated session", noun: "seated session", dataKey: DataKeySessions,
			wantMsg: "no seated sessions", wantData: map[string]any{DataKeyCount: uint64(0), DataKeyValue: uint64(0)},
		},
		{
			// The condition is what fires, so OK == true is the risk here.
			name: "seated sessions are named", noun: "seated session", dataKey: DataKeySessions,
			names: []string{"3 (alice on seat0)"}, wantOK: true, wantMsg: "1 seated session(s): 3 (alice on seat0)",
			wantData: map[string]any{DataKeyCount: uint64(1), DataKeyValue: uint64(1), DataKeySessions: "3 (alice on seat0)"},
		},
		{
			name: "users without linger are named", noun: "unlingered user", dataKey: DataKeyUsers,
			names: []string{"alice", "root"}, wantOK: true, wantMsg: "2 unlingered user(s): alice, root",
			wantData: map[string]any{DataKeyCount: uint64(2), DataKeyValue: uint64(2), DataKeyUsers: "alice, root"},
		},
		{
			// A record it cannot read must not read as "nothing at risk".
			name: "unreadable records", noun: "unlingered user", dataKey: DataKeyUsers,
			err: errors.New("user 0: record has no NAME"), wantUnavailable: true, wantMsg: "record has no NAME",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := logindNamesCheck{
				name: "l", timeout: time.Second, checkType: "fixture", noun: tc.noun, dataKey: tc.dataKey, op: ">", value: 0,
				sampler: func() ([]string, error) { return tc.names, tc.err },
			}
			res := check.Run(context.Background())
			if res.OK != tc.wantOK || res.Unavailable != tc.wantUnavailable {
				t.Fatalf("OK = %v, Unavailable = %v; want %v, %v (%s)", res.OK, res.Unavailable, tc.wantOK, tc.wantUnavailable, res.Message)
			}
			if !strings.Contains(res.Message, tc.wantMsg) {
				t.Fatalf("message = %q, want substring %q", res.Message, tc.wantMsg)
			}
			if tc.wantData != nil && !reflect.DeepEqual(res.Data, tc.wantData) {
				t.Fatalf("data = %#v\nwant   %#v", res.Data, tc.wantData)
			}
		})
	}
}

func TestBuildLogindNamesChecks(t *testing.T) {
	for _, build := range []func(base, map[string]any) (Check, string){buildSeatedSessionsCheck, buildUsersWithoutLingerCheck} {
		check, errs := build(base{name: "l"}, map[string]any{})
		if built, ok := check.(logindNamesCheck); errs != "" || !ok || built.op != ">" || built.value != 0 || built.sampler == nil {
			t.Fatalf("default = %+v, %q; want it to fire on any entry", check, errs)
		}
		check, errs = build(base{name: "l"}, map[string]any{CheckKeyCount: map[string]any{CheckKeyOp: ">=", CheckKeyValue: 2}})
		if built, ok := check.(logindNamesCheck); errs != "" || !ok || built.op != ">=" || built.value != 2 {
			t.Fatalf("explicit count = %+v, %q", check, errs)
		}
		if _, errs = build(base{name: "l"}, map[string]any{CheckKeyCount: "many"}); !strings.Contains(errs, "count") {
			t.Fatalf("errs = %q, want the malformed count rejected", errs)
		}
	}
}
