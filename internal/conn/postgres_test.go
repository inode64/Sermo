package conn

import (
	"net/url"
	"testing"
)

func TestPostgresRegistered(t *testing.T) {
	for _, name := range []string{"postgres", "postgresql"} {
		p, ok := Lookup(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if p.DefaultPort() != 5432 {
			t.Fatalf("%s default port = %d, want 5432", name, p.DefaultPort())
		}
	}
}

func TestBuildPGDSN(t *testing.T) {
	dsn := buildPGDSN(Config{
		Host: "db.example", Port: 5433, User: "monitor",
		Password: "p@ss:w/rd", Database: "app", TLS: "verify-full",
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse %q: %v", dsn, err)
	}
	if u.Scheme != "postgres" || u.Host != "db.example:5433" {
		t.Fatalf("scheme/host = %s %q", u.Scheme, u.Host)
	}
	if u.User.Username() != "monitor" {
		t.Fatalf("user = %q", u.User.Username())
	}
	pw, _ := u.User.Password()
	if pw != "p@ss:w/rd" {
		t.Fatalf("password = %q (escaping wrong)", pw)
	}
	if u.Path != "/app" {
		t.Fatalf("path = %q, want /app", u.Path)
	}
	if u.Query().Get("sslmode") != "verify-full" {
		t.Fatalf("sslmode = %q", u.Query().Get("sslmode"))
	}
}

func TestBuildPGDSNDefaults(t *testing.T) {
	u, err := url.Parse(buildPGDSN(Config{User: "u"}))
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "127.0.0.1:5432" {
		t.Fatalf("host = %q, want default 127.0.0.1:5432", u.Host)
	}
	if u.Query().Get("sslmode") != "disable" {
		t.Fatalf("sslmode = %q, want disable by default (plaintext)", u.Query().Get("sslmode"))
	}
}

func TestSSLMode(t *testing.T) {
	cases := map[string]string{
		"": "disable", "false": "disable", "off": "disable",
		"true": "require", "on": "require",
		"skip-verify": "require", "insecure": "require",
		"verify-full": "verify-full", "verify-ca": "verify-ca", "prefer": "prefer",
	}
	for in, want := range cases {
		if got := sslMode(in); got != want {
			t.Errorf("sslMode(%q) = %q, want %q", in, got, want)
		}
	}
}
