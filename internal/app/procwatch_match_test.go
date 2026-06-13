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
		if got := procMatches(c.m, id); got != c.want {
			t.Errorf("%s: procMatches(%+v) = %v, want %v", c.desc, c.m, got, c.want)
		}
	}
	// A name selector must not match a process whose exe could not be resolved
	// (an unverified exe must never satisfy safe matching).
	unresolved := process.Identity{Exe: "/usr/sbin/nginx", ExeOK: false, User: "www"}
	if procMatches(ProcMatch{Name: "nginx"}, unresolved) {
		t.Error("a name selector must not match an unresolved exe")
	}
}
