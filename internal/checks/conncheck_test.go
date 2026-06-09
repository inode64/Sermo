package checks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"sermo/internal/conn"
)

type fakeProto struct{}

func (fakeProto) Name() string       { return "mysql" }
func (fakeProto) DefaultPort() int   { return 3306 }
func (fakeProto) RequiresUser() bool { return true }
func (fakeProto) Probe(context.Context, conn.Config) (conn.Result, error) {
	return conn.Result{}, nil
}

func TestConnCheckRunOKWithVersion(t *testing.T) {
	var gotCfg conn.Config
	c := connCheck{
		base:  base{name: "db", timeout: time.Second},
		proto: fakeProto{},
		cfg:   conn.Config{Host: "127.0.0.1", Port: 3306, User: "monitor"},
		probe: func(_ context.Context, cfg conn.Config) (conn.Result, error) {
			gotCfg = cfg
			return conn.Result{Version: "8.0.36"}, nil
		},
	}
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("expected OK, got %q", res.Message)
	}
	if gotCfg.User != "monitor" {
		t.Fatalf("probe got cfg %+v", gotCfg)
	}
	if res.Data["version"] != "8.0.36" || res.Data["protocol"] != "mysql" {
		t.Fatalf("data = %v", res.Data)
	}
	if !strings.Contains(res.Message, "8.0.36") {
		t.Fatalf("message should carry version: %q", res.Message)
	}
}

func TestConnCheckOnChangeFingerprint(t *testing.T) {
	fp := "SHA256:aaa"
	c := connCheck{
		base:  base{name: "ssh", timeout: time.Second},
		proto: fakeProto{},
		cfg:   conn.Config{Host: "h", Port: 22},
		probe: func(context.Context, conn.Config) (conn.Result, error) {
			return conn.Result{Extra: map[string]string{"fingerprint": fp}}, nil
		},
		onChange: true,
		state:    &connState{},
	}
	// First cycle primes; no alert.
	if res := c.Run(context.Background()); !res.OK {
		t.Fatalf("first cycle must prime, not alert: %q", res.Message)
	}
	// Same fingerprint: still ok.
	if res := c.Run(context.Background()); !res.OK {
		t.Fatalf("unchanged fingerprint must stay ok: %q", res.Message)
	}
	// Fingerprint changes: alert (fails).
	fp = "SHA256:bbb"
	res := c.Run(context.Background())
	if res.OK {
		t.Fatal("a changed fingerprint must fail the check")
	}
	if res.Data["fingerprint_old"] != "SHA256:aaa" || res.Data["fingerprint"] != "SHA256:bbb" {
		t.Fatalf("data should carry old/new: %v", res.Data)
	}
	// After alerting once, the new value becomes the baseline (no repeat alert).
	if res := c.Run(context.Background()); !res.OK {
		t.Fatalf("must not keep alerting on a stable fingerprint: %q", res.Message)
	}
}

func TestConnCheckRunFailure(t *testing.T) {
	c := connCheck{
		base:  base{name: "db", timeout: time.Second},
		proto: fakeProto{},
		cfg:   conn.Config{Host: "db", Port: 3306, User: "u"},
		probe: func(context.Context, conn.Config) (conn.Result, error) {
			return conn.Result{}, errors.New("access denied")
		},
	}
	res := c.Run(context.Background())
	if res.OK {
		t.Fatal("a probe error must fail the check")
	}
	if !strings.Contains(res.Message, "access denied") {
		t.Fatalf("message should carry the error: %q", res.Message)
	}
}

func TestBuildMySQLCheck(t *testing.T) {
	built, warns := Build(map[string]any{
		"db": map[string]any{
			"type": "mysql", "user": "monitor", "password": "secret",
			"host": "10.0.0.5", "port": 3307, "tls": "skip-verify",
		},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("expected a clean build, warns=%v built=%d", warns, len(built))
	}
	cc, ok := built[0].Check.(connCheck)
	if !ok {
		t.Fatalf("expected connCheck, got %T", built[0].Check)
	}
	if cc.proto.Name() != "mysql" || cc.cfg.Host != "10.0.0.5" || cc.cfg.Port != 3307 {
		t.Fatalf("cfg = %+v", cc.cfg)
	}
	if cc.cfg.User != "monitor" || cc.cfg.Password != "secret" || cc.cfg.TLS != "skip-verify" {
		t.Fatalf("creds/tls = %+v", cc.cfg)
	}
}

func TestBuildMySQLCheckDefaultsAndUserRequired(t *testing.T) {
	// mariadb alias resolves; missing user warns.
	_, warns := Build(map[string]any{
		"db": map[string]any{"type": "mariadb"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) == 0 || !strings.Contains(warns[0], "user") {
		t.Fatalf("missing user should warn, got %v", warns)
	}

	built, warns := Build(map[string]any{
		"db": map[string]any{"type": "mariadb", "user": "u"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("mariadb with user should build: warns=%v", warns)
	}
	cc := built[0].Check.(connCheck)
	if cc.cfg.Host != "127.0.0.1" || cc.cfg.Port != 3306 {
		t.Fatalf("defaults = %+v, want 127.0.0.1:3306", cc.cfg)
	}
}

func TestBuildPostgresCheck(t *testing.T) {
	// The generic dispatch picks up any registered protocol — postgres needed no
	// change to buildCheck. Alias "postgresql" resolves too.
	for _, typ := range []string{"postgres", "postgresql"} {
		built, warns := Build(map[string]any{
			"db": map[string]any{"type": typ, "user": "monitor"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s should build cleanly: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "postgres" || cc.cfg.Port != 5432 || cc.cfg.Host != "127.0.0.1" {
			t.Fatalf("%s cfg = %+v (proto %s)", typ, cc.cfg, cc.proto.Name())
		}
	}
}

func TestBuildRedisCheckUserOptional(t *testing.T) {
	// redis does not require a user (password-only / no-auth) — must build with
	// just a password, and default to port 6379.
	built, warns := Build(map[string]any{
		"cache": map[string]any{"type": "redis", "password": "secret"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("redis with only a password should build: warns=%v", warns)
	}
	cc := built[0].Check.(connCheck)
	if cc.proto.Name() != "redis" || cc.cfg.Port != 6379 || cc.cfg.Password != "secret" {
		t.Fatalf("cfg = %+v", cc.cfg)
	}
}

func TestBuildIMAPCheckAnonymousAndLogin(t *testing.T) {
	// Anonymous: no user/password — must build (imap allows it), default port 143.
	built, warns := Build(map[string]any{
		"mail": map[string]any{"type": "imap", "host": "mail.example"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("anonymous imap should build: warns=%v", warns)
	}
	cc := built[0].Check.(connCheck)
	if cc.proto.Name() != "imap" || cc.cfg.Port != 143 || cc.cfg.Host != "mail.example" {
		t.Fatalf("cfg = %+v", cc.cfg)
	}

	// With credentials + implicit TLS on 993.
	built, warns = Build(map[string]any{
		"mail": map[string]any{"type": "imap", "port": 993, "tls": true, "user": "joe", "password": "p"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("imap with creds should build: warns=%v", warns)
	}
	cc = built[0].Check.(connCheck)
	if cc.cfg.Port != 993 || cc.cfg.User != "joe" || cc.cfg.TLS != "true" {
		t.Fatalf("cfg = %+v", cc.cfg)
	}
}

func TestBuildPOPCheck(t *testing.T) {
	// Anonymous (no user), default port 110; alias pop3 resolves.
	for _, typ := range []string{"pop", "pop3"} {
		built, warns := Build(map[string]any{
			"mail": map[string]any{"type": typ, "host": "mail.example"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s anonymous should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "pop" || cc.cfg.Port != 110 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
	}
}

func TestBuildSMTPCheck(t *testing.T) {
	// Anonymous (no user), default port 25.
	built, warns := Build(map[string]any{
		"mx": map[string]any{"type": "smtp", "host": "mail.example"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("anonymous smtp should build: warns=%v", warns)
	}
	cc := built[0].Check.(connCheck)
	if cc.proto.Name() != "smtp" || cc.cfg.Port != 25 {
		t.Fatalf("cfg = %+v", cc.cfg)
	}

	// Submission with credentials on 587.
	built, warns = Build(map[string]any{
		"mx": map[string]any{"type": "smtp", "port": 587, "user": "joe", "password": "p"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("smtp with creds should build: warns=%v", warns)
	}
	if built[0].Check.(connCheck).cfg.Port != 587 {
		t.Fatalf("port not parsed")
	}
}

func TestBuildFPMCheck(t *testing.T) {
	// Unix socket form: no user; socket carried into the config and reflected in
	// the result addr.
	built, warns := Build(map[string]any{
		"php": map[string]any{"type": "fpm", "socket": "/run/php/php8.2-fpm.sock"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("fpm socket check should build: warns=%v", warns)
	}
	cc := built[0].Check.(connCheck)
	if cc.proto.Name() != "fpm" || cc.cfg.Socket != "/run/php/php8.2-fpm.sock" {
		t.Fatalf("cfg = %+v", cc.cfg)
	}

	// TCP form via the php-fpm alias, default port 9000.
	built, _ = Build(map[string]any{
		"php": map[string]any{"type": "php-fpm", "host": "127.0.0.1"},
	}, Deps{DefaultTimeout: time.Second})
	if len(built) != 1 {
		t.Fatal("php-fpm alias should build")
	}
	if built[0].Check.(connCheck).cfg.Port != 9000 {
		t.Fatal("fpm default tcp port should be 9000")
	}
}

func TestBuildDNSCheck(t *testing.T) {
	built, warns := Build(map[string]any{
		"resolver": map[string]any{"type": "dns", "host": "1.1.1.1", "query": "example.com"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("dns check should build: warns=%v", warns)
	}
	cc := built[0].Check.(connCheck)
	if cc.proto.Name() != "dns" || cc.cfg.Port != 53 || cc.cfg.Query != "example.com" {
		t.Fatalf("cfg = %+v", cc.cfg)
	}
}

func TestBuildFTPCheck(t *testing.T) {
	// Anonymous (no user), default port 21.
	built, warns := Build(map[string]any{
		"ftp": map[string]any{"type": "ftp", "host": "ftp.example"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("anonymous ftp should build: warns=%v", warns)
	}
	cc := built[0].Check.(connCheck)
	if cc.proto.Name() != "ftp" || cc.cfg.Port != 21 {
		t.Fatalf("cfg = %+v", cc.cfg)
	}
	// With credentials + implicit FTPS on 990.
	built, _ = Build(map[string]any{
		"ftp": map[string]any{"type": "ftp", "port": 990, "tls": true, "user": "joe", "password": "p"},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(connCheck); cc.cfg.Port != 990 || cc.cfg.User != "joe" || cc.cfg.TLS != "true" {
		t.Fatalf("cfg = %+v", cc.cfg)
	}
}

func TestBuildSSHCheck(t *testing.T) {
	// Anonymous (no user), default port 22, with fingerprint-change detection.
	built, warns := Build(map[string]any{
		"ssh": map[string]any{"type": "ssh", "host": "host.example", "on_change": true},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("anonymous ssh should build: warns=%v", warns)
	}
	cc := built[0].Check.(connCheck)
	if cc.proto.Name() != "ssh" || cc.cfg.Port != 22 {
		t.Fatalf("cfg = %+v", cc.cfg)
	}
	if !cc.onChange || cc.state == nil {
		t.Fatal("on_change must enable stateful fingerprint detection")
	}

	// With credentials.
	built, _ = Build(map[string]any{
		"ssh": map[string]any{"type": "ssh", "host": "host.example", "user": "admin", "password": "p"},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(connCheck); cc.cfg.User != "admin" || cc.cfg.Password != "p" {
		t.Fatalf("cfg = %+v", cc.cfg)
	}
}

func TestBuildNTPCheck(t *testing.T) {
	built, warns := Build(map[string]any{
		"clock": map[string]any{"type": "ntp", "host": "pool.ntp.org"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("ntp check should build: warns=%v", warns)
	}
	cc := built[0].Check.(connCheck)
	if cc.proto.Name() != "ntp" || cc.cfg.Port != 123 {
		t.Fatalf("cfg = %+v", cc.cfg)
	}
}

func TestBuildSNMPCheck(t *testing.T) {
	// v2c anonymous (community), default port 161, with identity-change detection.
	built, warns := Build(map[string]any{
		"router": map[string]any{"type": "snmp", "host": "10.0.0.1", "on_change": true},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("snmp v2c should build: warns=%v", warns)
	}
	cc := built[0].Check.(connCheck)
	if cc.proto.Name() != "snmp" || cc.cfg.Port != 161 || !cc.onChange {
		t.Fatalf("cfg = %+v onChange=%v", cc.cfg, cc.onChange)
	}
	// v3 with user/password.
	built, _ = Build(map[string]any{
		"router": map[string]any{"type": "snmp", "host": "10.0.0.1", "user": "monitor", "password": "p"},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(connCheck); cc.cfg.User != "monitor" || cc.cfg.Password != "p" {
		t.Fatalf("cfg = %+v", cc.cfg)
	}
}

func TestBuildUnknownTypeStillWarns(t *testing.T) {
	_, warns := Build(map[string]any{
		"x": map[string]any{"type": "nope"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) == 0 || !strings.Contains(warns[0], "nope") {
		t.Fatalf("unknown type should still warn, got %v", warns)
	}
}
