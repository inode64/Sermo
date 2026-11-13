package app

import (
	"testing"

	"sermo/internal/process"
)

func TestProcMatches(t *testing.T) {
	id := process.Identity{Exe: "/usr/sbin/nginx", ExeOK: true, User: "www"}
	cases := []struct {
		desc string
		m    ProcMatch
		want bool
	}{
		{"full exe path", ProcMatch{Name: "/usr/sbin/nginx"}, true},
		{"basename", ProcMatch{Name: "nginx"}, true},
		{"wrong name", ProcMatch{Name: "httpd"}, false},
		{"user only", ProcMatch{User: "www"}, true},
		{"wrong user", ProcMatch{User: "root"}, false},
		{"name and user", ProcMatch{Name: "nginx", User: "www"}, true},
		{"name ok but user wrong", ProcMatch{Name: "nginx", User: "root"}, false},
		{"empty selector matches nothing", ProcMatch{}, false},
	}
	for _, c := range cases {
		if got, known := procMatchesWithLookup(c.m, id, nil); got != c.want || !known {
			t.Errorf("%s: procMatchesWithLookup(%+v) = %v, want %v", c.desc, c.m, got, c.want)
		}
	}
	// A name selector must not match a process whose exe could not be resolved
	// (an unverified exe must never satisfy safe matching).
	unresolved := process.Identity{Exe: "/usr/sbin/nginx", ExeOK: false, User: "www"}
	if matched, known := procMatchesWithLookup(ProcMatch{Name: "nginx"}, unresolved, nil); matched || !known {
		t.Error("a name selector must not match an unresolved exe")
	}
}

// A replaced binary is matched by its previous path only when the caller asks
// to observe it: the path proves nothing about what the process runs now.
func TestProcMatchesReplacedBinaryOnlyForObservation(t *testing.T) {
	replaced := process.Identity{ExePrev: "/usr/sbin/nginx", User: "www"}
	cases := []struct {
		desc string
		m    ProcMatch
		want bool
	}{
		{"strict selector", ProcMatch{Name: "nginx"}, false},
		{"observed by basename", ProcMatch{Name: "nginx", Replaced: true}, true},
		{"observed by full path", ProcMatch{Name: "/usr/sbin/nginx", Replaced: true}, true},
		{"observed under another name", ProcMatch{Name: "httpd", Replaced: true}, false},
		{"observed for another user", ProcMatch{Name: "nginx", User: "root", Replaced: true}, false},
	}
	for _, c := range cases {
		if got, known := procMatchesWithLookup(c.m, replaced, nil); got != c.want || !known {
			t.Errorf("%s: procMatchesWithLookup(%+v) = %v, want %v", c.desc, c.m, got, c.want)
		}
	}
	unresolved := process.Identity{User: "www"}
	if matched, known := procMatchesWithLookup(ProcMatch{Name: "nginx", Replaced: true}, unresolved, nil); matched || !known {
		t.Error("an exe with no previous path must not match")
	}
}

func TestProcMatchesWithLookupAllowsNumericUser(t *testing.T) {
	id := process.Identity{Exe: "/usr/sbin/nginx", ExeOK: true, UID: 1001, User: "www"}
	lookup := process.NewUserLookup(process.UserLookupConfig{Mode: process.UserLookupNumeric})
	if matched, known := procMatchesWithLookup(ProcMatch{Name: "nginx", User: "1001"}, id, lookup); !matched || !known {
		t.Fatal("numeric process-watch user should match real UID")
	}
	if matched, known := procMatchesWithLookup(ProcMatch{Name: "nginx", User: "1002"}, id, lookup); matched || !known {
		t.Fatal("wrong numeric process-watch user matched")
	}
}
