package conn

import (
	"context"
	"net"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
)

func TestSSLModeSharesTheFriendlySpellings(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"": "disable", "false": "disable", "No": "disable", "off": "disable", "disable": "disable",
		"true": "verify-full", "yes": "verify-full", "ON": "verify-full", "required": "verify-full", "verify-full": "verify-full",
		"skip-verify": "require", "require": "require", " Prefer ": "prefer", "verify-ca": "verify-ca",
	} {
		if got := sslMode(in); got != want {
			t.Errorf("sslMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildPGDSN(t *testing.T) {
	dsn := postgresDSNForTest(Config{
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
	u, err := url.Parse(postgresDSNForTest(Config{User: "u"}))
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
	runMapCases(t, "sslMode", sslMode, map[string]string{
		"": "disable", "false": "disable", "off": "disable",
		"true": "verify-full", "on": "verify-full", "required": "verify-full",
		"require":     "require",
		"skip-verify": "require",
		"verify-full": "verify-full", "verify-ca": "verify-ca", "prefer": "prefer",
	})
}

func TestValidTLSValueSSLModesArePostgresOnly(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		value    string
		want     bool
	}{
		{name: "postgres sslmode", protocol: ProtocolNamePostgres, value: "verify-full", want: true},
		{name: "postgres alias sslmode", protocol: protocolAliasPostgreSQL, value: "disable", want: true},
		{name: "postgres shared spelling", protocol: ProtocolNamePostgres, value: "required", want: true},
		{name: "redis rejects disable", protocol: ProtocolNameRedis, value: "disable", want: false},
		{name: "mysql rejects verify-ca", protocol: ProtocolNameMySQL, value: "verify-ca", want: false},
		{name: "http probe rejects prefer", protocol: ProtocolNamePrometheus, value: "prefer", want: false},
		{name: "shared skip-verify", protocol: ProtocolNameLDAP, value: "skip-verify", want: true},
		{name: "unknown protocol shared only", protocol: "missing", value: "require", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidTLSValue(tt.protocol, tt.value); got != tt.want {
				t.Errorf("ValidTLSValue(%q, %q) = %v, want %v", tt.protocol, tt.value, got, tt.want)
			}
		})
	}
}

// A socket target reaches the server through the configured socket file, with
// no TLS negotiation even when tls is set: the server offers none there.
func TestPostgresProbeOverUnixSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), ".s.PGSQL.5432")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	first := make(chan pgproto3.FrontendMessage, 1)
	go func() {
		c, acceptErr := listener.Accept()
		if acceptErr != nil {
			close(first)
			return
		}
		defer func() { _ = c.Close() }()
		backend := pgproto3.NewBackend(c, c)
		msg, receiveErr := backend.ReceiveStartupMessage()
		if receiveErr != nil {
			close(first)
			return
		}
		first <- msg
		backend.Send(&pgproto3.ErrorResponse{Severity: "FATAL", Code: "53300", Message: "sorry, too many clients already"})
		_ = backend.Flush()
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	protocol, cfg, ok := Prepare(ProtocolNamePostgres, Config{Socket: socket, User: "root", Database: "postgres", TLS: "verify-full"})
	if !ok {
		t.Fatal("postgres is not registered")
	}
	res, err := protocol.Probe(ctx, cfg)
	assertBusy(t, res, err, "53300")
	startup, isStartup := (<-first).(*pgproto3.StartupMessage)
	if !isStartup || startup.Parameters["user"] != "root" {
		t.Fatalf("first message = %+v, want root's StartupMessage without an SSLRequest", startup)
	}

	missing := Config{Socket: filepath.Join(t.TempDir(), ".s.PGSQL.5432"), User: "root"}
	if _, err := protocol.Probe(ctx, missing); err == nil {
		t.Fatal("a missing socket must fail the probe")
	}
}

func postgresDSNForTest(cfg Config) string {
	return buildPGDSNWithTarget(newProbeTarget(cfg, defaultPortPostgres))
}
