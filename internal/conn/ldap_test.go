package conn

import "testing"

func TestLDAPRegistered(t *testing.T) {
	p, ok := Lookup("ldap")
	if !ok {
		t.Fatal("ldap not registered")
	}
	if p.DefaultPort() != 389 {
		t.Fatalf("default port = %d, want 389", p.DefaultPort())
	}
	if p.RequiresUser() {
		t.Fatal("ldap must not require a user (anonymous bind allowed)")
	}
}

func TestBuildLDAPURL(t *testing.T) {
	url, useTLS := buildLDAPURL("dir.example", 389, "")
	if url != "ldap://dir.example:389" || useTLS {
		t.Fatalf("plain = %q tls=%v", url, useTLS)
	}
	url, useTLS = buildLDAPURL("dir.example", 636, "true")
	if url != "ldaps://dir.example:636" || !useTLS {
		t.Fatalf("ldaps = %q tls=%v", url, useTLS)
	}
	if u, tls := buildLDAPURL("d", 636, "skip-verify"); !tls || u != "ldaps://d:636" {
		t.Fatalf("skip-verify should be ldaps: %q %v", u, tls)
	}
}

func TestLDAPSucceeds(t *testing.T) {
	// Anonymous: success if the server responded at all (bind ok OR an LDAP-level
	// rejection), not on a network error.
	if !ldapSucceeds(true, true, false) {
		t.Fatal("anonymous bind ok must succeed")
	}
	if !ldapSucceeds(false, true, false) {
		t.Fatal("anonymous: an LDAP rejection still proves the server is up")
	}
	if ldapSucceeds(false, false, false) {
		t.Fatal("anonymous: a network error must fail")
	}
	// Credentialed: the bind must succeed.
	if ldapSucceeds(false, true, true) {
		t.Fatal("credentialed: a bind rejection must fail")
	}
	if !ldapSucceeds(true, true, true) {
		t.Fatal("credentialed: a successful bind must pass")
	}
}
