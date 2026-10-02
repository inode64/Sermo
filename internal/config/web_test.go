package config

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWebBind(t *testing.T) {
	tests := []struct {
		name     string
		web      any
		want     WebBind
		wantAddr string
		wantErr  string
		wantIs   error
	}{
		{name: "no web section", wantErr: "no [web] section in config", wantIs: ErrWebNotConfigured},
		{name: "port missing", web: map[string]any{}, wantErr: "web.port is not set", wantIs: ErrWebPortUnset},
		{name: "default address", web: map[string]any{WebKeyPort: 9797}, want: WebBind{Host: "127.0.0.1", Port: 9797}, wantAddr: "127.0.0.1:9797"},
		{name: "empty address uses default", web: map[string]any{WebKeyAddress: "", WebKeyPort: 9797}, want: WebBind{Host: "127.0.0.1", Port: 9797}, wantAddr: "127.0.0.1:9797"},
		{name: "IPv4 wildcard", web: map[string]any{WebKeyAddress: "0.0.0.0", WebKeyPort: 9797}, want: WebBind{Host: "0.0.0.0", Port: 9797}, wantAddr: "0.0.0.0:9797"},
		{name: "IPv6 loopback", web: map[string]any{WebKeyAddress: "::1", WebKeyPort: 9797}, want: WebBind{Host: "::1", Port: 9797}, wantAddr: "[::1]:9797"},
		{name: "quoted port accepted", web: map[string]any{WebKeyPort: "8080"}, want: WebBind{Host: "127.0.0.1", Port: 8080}, wantAddr: "127.0.0.1:8080"},
		{name: "port zero", web: map[string]any{WebKeyPort: 0}, wantErr: "web.port must be in 1..65535 (got 0)"},
		{name: "port above range", web: map[string]any{WebKeyPort: 65536}, wantErr: "web.port must be in 1..65535 (got 65536)"},
		{name: "port not a number", web: map[string]any{WebKeyPort: "abc"}, wantErr: "web.port is not a number (string)"},
		{name: "address not a string", web: map[string]any{WebKeyAddress: 7, WebKeyPort: 9797}, wantErr: "web.address must be a string (got int)"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := map[string]any{}
			if tc.web != nil {
				raw[SectionWeb] = tc.web
			}
			got, err := (Global{Raw: raw}).WebBind()
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("WebBind() error = %v, want %q", err, tc.wantErr)
				}
				if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
					t.Errorf("errors.Is(%v, %v) = false", err, tc.wantIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("WebBind() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("WebBind() = %#v, want %#v", got, tc.want)
			}
			if addr := got.HostPort(); addr != tc.wantAddr {
				t.Errorf("HostPort() = %q, want %q", addr, tc.wantAddr)
			}
		})
	}
}

func TestValidateWebBlock(t *testing.T) {
	mustNotHave(t, validateGlobalDoc(t, `
web: { address: 127.0.0.1, port: 9797 }
paths: { services: [ @ROOT@/services ] }
defaults: { policy: { cooldown: 5m } }
`), "web.")

	issues := validateGlobalDoc(t, `
web: { port: 70000, address: 5 }
paths: { services: [ @ROOT@/services ] }
defaults: { policy: { cooldown: 5m } }
`)
	if !hasIssue(issues, "web.port must be an integer in 1..65535") {
		t.Fatalf("missing web.port issue in %v", issues)
	}
	if !hasIssue(issues, "web.address must be a string") {
		t.Fatalf("missing web.address issue in %v", issues)
	}

	mustNotHave(t, validateGlobalDoc(t, `
web: { address: 127.0.0.1 }
paths: { services: [ @ROOT@/services ] }
defaults: { policy: { cooldown: 5m } }
`), "web.")
}

// web.public_url is the dashboard address notifications link to: an absolute
// http(s) URL, optionally under a proxy path, with no query or fragment.
func TestWebPublicURL(t *testing.T) {
	for _, bad := range []string{`"fr5.intranet:9797"`, `"ftp://h/"`, `"http://h:9797/#wat:x"`, `"http://h/?a=1"`, `5`} {
		issues := validateGlobalDoc(t, `
web: { port: 9797, public_url: `+bad+` }
paths: { services: [ @ROOT@/services ] }
defaults: { policy: { cooldown: 5m } }
`)
		if !hasIssue(issues, "web.public_url") {
			t.Errorf("public_url %s: no issue in %v", bad, issues)
		}
	}
	for raw, want := range map[string]string{
		"http://fr5.intranet:9797/":         "http://fr5.intranet:9797",
		"https://ops.example.com/sermo/fr5": "https://ops.example.com/sermo/fr5",
		"":                                  "",
	} {
		g := Global{Raw: map[string]any{SectionWeb: map[string]any{WebKeyPublicURL: raw}}}
		if got := g.WebPublicURL(); got != want {
			t.Errorf("WebPublicURL(%q) = %q, want %q", raw, got, want)
		}
	}
	if got := (Global{}).WebPublicURL(); got != "" {
		t.Errorf("WebPublicURL without a web block = %q", got)
	}
}

// web.session_ttl is how long a dashboard login lasts: a positive duration;
// unset leaves the web server's default.
func TestWebSessionTTL(t *testing.T) {
	for _, bad := range []string{`"forever"`, `"-1h"`, `"0s"`, `5`} {
		issues := validateGlobalDoc(t, `
web: { port: 9797, session_ttl: `+bad+` }
paths: { services: [ @ROOT@/services ] }
defaults: { policy: { cooldown: 5m } }
`)
		if !hasIssue(issues, "web.session_ttl") {
			t.Errorf("session_ttl %s: no issue in %v", bad, issues)
		}
	}
	if issues := validateGlobalDoc(t, `
web: { port: 9797, session_ttl: 8h }
paths: { services: [ @ROOT@/services ] }
defaults: { policy: { cooldown: 5m } }
`); hasIssue(issues, "web.session_ttl") {
		t.Errorf("session_ttl 8h: unexpected issue in %v", issues)
	}
	for raw, want := range map[any]time.Duration{"8h": 8 * time.Hour, "nope": 0} {
		g := Global{Raw: map[string]any{SectionWeb: map[string]any{WebKeySessionTTL: raw}}}
		if got := g.WebSessionTTL(); got != want {
			t.Errorf("WebSessionTTL(%v) = %v, want %v", raw, got, want)
		}
	}
	if got := (Global{}).WebSessionTTL(); got != 0 {
		t.Errorf("WebSessionTTL without a web block = %v, want 0 (server default)", got)
	}
}

// web.login_message is one short plain-text line under the host on the login
// page.
func TestWebLoginMessage(t *testing.T) {
	for _, bad := range []string{`5`, `"` + strings.Repeat("x", maxLoginMessageRunes+1) + `"`, `"two\nlines"`} {
		issues := validateGlobalDoc(t, `
web: { port: 9797, login_message: `+bad+` }
paths: { services: [ @ROOT@/services ] }
defaults: { policy: { cooldown: 5m } }
`)
		if !hasIssue(issues, "web.login_message") {
			t.Errorf("login_message %.20s…: no issue in %v", bad, issues)
		}
	}
	if issues := validateGlobalDoc(t, `
web: { port: 9797, login_message: "Producción · Takeachef" }
paths: { services: [ @ROOT@/services ] }
defaults: { policy: { cooldown: 5m } }
`); hasIssue(issues, "web.login_message") {
		t.Errorf("valid login_message: unexpected issue in %v", issues)
	}
	g := Global{Raw: map[string]any{SectionWeb: map[string]any{WebKeyLoginMessage: "  Solo sysadmins  "}}}
	if got := g.WebLoginMessage(); got != "Solo sysadmins" {
		t.Errorf("WebLoginMessage = %q, want it trimmed", got)
	}
	if got := (Global{}).WebLoginMessage(); got != "" {
		t.Errorf("WebLoginMessage without a web block = %q", got)
	}
}
