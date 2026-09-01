package conn

import "testing"

func TestBuildLDAPURL(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		port    int
		tlsMode string
		wantURL string
		wantTLS bool
	}{
		{name: "plain", host: "dir.example", port: 389, wantURL: "ldap://dir.example:389"},
		{name: "TLS", host: "dir.example", port: 636, tlsMode: "true", wantURL: "ldaps://dir.example:636", wantTLS: true},
		{name: "skip verify", host: "d", port: 636, tlsMode: "skip-verify", wantURL: "ldaps://d:636", wantTLS: true},
		{name: "IPv6", host: "2001:db8::1", port: 636, tlsMode: "true", wantURL: "ldaps://[2001:db8::1]:636", wantTLS: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotURL, gotTLS := buildLDAPURL(test.host, test.port, test.tlsMode)
			if gotURL != test.wantURL || gotTLS != test.wantTLS {
				t.Fatalf("buildLDAPURL() = %q, %v; want %q, %v", gotURL, gotTLS, test.wantURL, test.wantTLS)
			}
		})
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
