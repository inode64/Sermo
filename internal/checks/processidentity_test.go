package checks

import (
	"strings"
	"testing"

	"sermo/internal/process"
)

// The check fails only for the verdict an operation refuses, and its message
// carries the evidence and the remedy.
func TestProcessIdentityCheckFailsOnlyForAnUnverifiedIdentity(t *testing.T) {
	const detail = "main wants exe=/usr/bin/pm2 user=root; running: pid 4642 exe=/usr/bin/node user=root"
	for _, tc := range []struct {
		name     string
		identity process.PrincipalIdentity
		wantOK   bool
		want     string
	}{
		{name: "selector matches nothing", identity: process.PrincipalIdentity{Attributed: true, Required: true, Detail: detail}, want: detail},
		{name: "trusted daemon", identity: process.PrincipalIdentity{Attributed: true, Required: true, Trusted: true}, wantOK: true, want: "match the main selector"},
		{name: "replaced binary", identity: process.PrincipalIdentity{Attributed: true, Required: true, ReplacedExecutable: true}, wantOK: true, want: "replaced binary"},
		{name: "no strict selector", identity: process.PrincipalIdentity{Attributed: true}, wantOK: true, want: "no strict main selector"},
		{name: "nothing running", identity: process.PrincipalIdentity{Required: true}, wantOK: true, want: "no service process"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check, warn := buildProcessIdentityCheck(base{name: "process-identity"}, Deps{
				ProcessIdentity: func() process.PrincipalIdentity { return tc.identity },
			})
			if warn != "" {
				t.Fatal(warn)
			}
			res := check.Run(t.Context())
			if res.OK != tc.wantOK || !strings.Contains(res.Message, tc.want) {
				t.Fatalf("ok=%v message=%q, want ok=%v mentioning %q", res.OK, res.Message, tc.wantOK, tc.want)
			}
			if !tc.wantOK && !strings.Contains(res.Message, ProcessIdentityHint) {
				t.Fatalf("message = %q, want the remedy", res.Message)
			}
		})
	}
}

func TestProcessIdentityCheckNeedsProcessDiscovery(t *testing.T) {
	if _, warn := buildProcessIdentityCheck(base{name: "process-identity"}, Deps{}); warn == "" {
		t.Fatal("built without process discovery")
	}
}
