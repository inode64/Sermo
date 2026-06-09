package conn

import (
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestBuildDSN(t *testing.T) {
	dsn := buildDSN(Config{
		Host: "db.example", Port: 3307, User: "monitor",
		Password: "p@ss:w/rd", Database: "app", TLS: "skip-verify",
	})
	c, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("ParseDSN(%q): %v", dsn, err)
	}
	if c.User != "monitor" || c.Passwd != "p@ss:w/rd" {
		t.Fatalf("creds = %q/%q", c.User, c.Passwd)
	}
	if c.Net != "tcp" || c.Addr != "db.example:3307" {
		t.Fatalf("addr = %s %q", c.Net, c.Addr)
	}
	if c.DBName != "app" {
		t.Fatalf("db = %q", c.DBName)
	}
	if c.TLSConfig != "skip-verify" {
		t.Fatalf("tls = %q, want skip-verify", c.TLSConfig)
	}
}

func TestBuildDSNDefaultsAndPlaintext(t *testing.T) {
	dsn := buildDSN(Config{User: "u"})
	c, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != "127.0.0.1:3306" {
		t.Fatalf("addr = %q, want default 127.0.0.1:3306", c.Addr)
	}
	if c.TLSConfig != "" {
		t.Fatalf("tls = %q, want empty (plaintext) by default", c.TLSConfig)
	}
}

func TestNormalizeTLS(t *testing.T) {
	cases := map[string]string{
		"": "", "false": "", "no": "",
		"true": "true", "yes": "true",
		"skip-verify": "skip-verify", "insecure": "skip-verify",
	}
	for in, want := range cases {
		if got := normalizeTLS(in); got != want {
			t.Errorf("normalizeTLS(%q) = %q, want %q", in, got, want)
		}
	}
}
