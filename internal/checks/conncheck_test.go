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

func TestBuildNNTPCheck(t *testing.T) {
	// Anonymous (no user), default port 119; alias nntps resolves.
	for _, typ := range []string{"nntp", "nntps"} {
		built, warns := Build(map[string]any{
			"news": map[string]any{"type": typ, "host": "news.example"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s anonymous should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "nntp" || cc.cfg.Port != 119 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
	}

	// NNTPS with credentials on 563.
	built, warns := Build(map[string]any{
		"news": map[string]any{"type": "nntp", "port": 563, "tls": true, "user": "joe", "password": "p"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("nntps with creds should build: warns=%v", warns)
	}
	if cc := built[0].Check.(connCheck); cc.cfg.Port != 563 || cc.cfg.TLS != "true" || cc.cfg.User != "joe" {
		t.Fatalf("cfg = %+v", cc.cfg)
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

func TestBuildTFTPCheck(t *testing.T) {
	built, warns := Build(map[string]any{
		"pxe": map[string]any{"type": "tftp", "host": "10.0.0.2", "query": "pxelinux.0"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("tftp check should build: warns=%v", warns)
	}
	cc := built[0].Check.(connCheck)
	if cc.proto.Name() != "tftp" || cc.cfg.Port != 69 || cc.cfg.Query != "pxelinux.0" {
		t.Fatalf("cfg = %+v", cc.cfg)
	}
}

func TestBuildLDAPCheck(t *testing.T) {
	// Anonymous (no user), default port 389.
	built, warns := Build(map[string]any{
		"dir": map[string]any{"type": "ldap", "host": "ldap.example"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("anonymous ldap should build: warns=%v", warns)
	}
	cc := built[0].Check.(connCheck)
	if cc.proto.Name() != "ldap" || cc.cfg.Port != 389 {
		t.Fatalf("cfg = %+v", cc.cfg)
	}
	// LDAPS + simple bind.
	built, _ = Build(map[string]any{
		"dir": map[string]any{"type": "ldap", "host": "ldap.example", "port": 636, "tls": true,
			"user": "cn=monitor,dc=example,dc=com", "password": "p"},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(connCheck); cc.cfg.Port != 636 || cc.cfg.TLS != "true" || cc.cfg.User == "" {
		t.Fatalf("cfg = %+v", cc.cfg)
	}
}

func TestBuildAJPCheck(t *testing.T) {
	built, warns := Build(map[string]any{
		"tomcat": map[string]any{"type": "ajp", "host": "127.0.0.1"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("ajp check should build: warns=%v", warns)
	}
	cc := built[0].Check.(connCheck)
	if cc.proto.Name() != "ajp" || cc.cfg.Port != 8009 {
		t.Fatalf("cfg = %+v", cc.cfg)
	}
}

func TestBuildIPPCheck(t *testing.T) {
	for _, typ := range []string{"ipp", "cups"} {
		built, warns := Build(map[string]any{
			"printer": map[string]any{"type": typ, "host": "127.0.0.1"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "ipp" || cc.cfg.Port != 631 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
	}
}

func TestBuildRsyncCheck(t *testing.T) {
	for _, typ := range []string{"rsync", "rsyncd"} {
		built, warns := Build(map[string]any{
			"backup": map[string]any{"type": typ, "host": "127.0.0.1"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "rsync" || cc.cfg.Port != 873 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
	}
}

func TestBuildSyncthingCheck(t *testing.T) {
	// Anonymous health check: no user, default port 8384.
	built, warns := Build(map[string]any{
		"sync": map[string]any{"type": "syncthing", "host": "127.0.0.1"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("syncthing check should build: warns=%v", warns)
	}
	cc := built[0].Check.(connCheck)
	if cc.proto.Name() != "syncthing" || cc.cfg.Port != 8384 {
		t.Fatalf("cfg = %+v", cc.cfg)
	}

	// API key (password) + HTTPS skip-verify carried through.
	built, _ = Build(map[string]any{
		"sync": map[string]any{"type": "syncthing", "password": "the-key", "tls": "skip-verify"},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(connCheck); cc.cfg.Password != "the-key" || cc.cfg.TLS != "skip-verify" {
		t.Fatalf("cfg = %+v", cc.cfg)
	}
}

func TestBuildPrometheusCheck(t *testing.T) {
	for _, typ := range []string{"prometheus", "prom"} {
		built, warns := Build(map[string]any{
			"mon": map[string]any{"type": typ, "host": "127.0.0.1"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "prometheus" || cc.cfg.Port != 9090 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
	}
}

func TestBuildInfluxdbCheck(t *testing.T) {
	for _, typ := range []string{"influxdb", "influx"} {
		built, warns := Build(map[string]any{
			"tsdb": map[string]any{"type": typ, "host": "127.0.0.1"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "influxdb" || cc.cfg.Port != 8086 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
	}
	// https via tls is carried through.
	built, _ := Build(map[string]any{
		"tsdb": map[string]any{"type": "influxdb", "host": "127.0.0.1", "tls": "skip-verify"},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(connCheck); cc.cfg.TLS != "skip-verify" {
		t.Fatalf("tls = %q", cc.cfg.TLS)
	}
}

func TestBuildUnifiCheck(t *testing.T) {
	for _, typ := range []string{"unifi", "unifi-controller", "unifi-network"} {
		built, warns := Build(map[string]any{
			"unifi": map[string]any{"type": typ, "host": "10.0.0.1"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "unifi" || cc.cfg.Port != 8443 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
	}
	// tls: true (require a valid certificate) is carried through.
	built, _ := Build(map[string]any{
		"unifi": map[string]any{"type": "unifi", "host": "10.0.0.1", "tls": true},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(connCheck); cc.cfg.TLS != "true" {
		t.Fatalf("tls = %q", cc.cfg.TLS)
	}
}

func TestBuildDBusCheck(t *testing.T) {
	// No socket/query -> the system bus default address; default port 0.
	built, warns := Build(map[string]any{
		"bus": map[string]any{"type": "dbus"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("dbus check should build: warns=%v", warns)
	}
	cc := built[0].Check.(connCheck)
	if cc.proto.Name() != "dbus" || cc.cfg.Port != 0 {
		t.Fatalf("cfg = %+v", cc.cfg)
	}
	if cc.cfg.Socket != "unix:path=/var/run/dbus/system_bus_socket" {
		t.Fatalf("default address = %q", cc.cfg.Socket)
	}

	// A socket path is wrapped as unix:path=.
	built, _ = Build(map[string]any{
		"bus": map[string]any{"type": "dbus", "socket": "/run/dbus/system_bus_socket"},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(connCheck); cc.cfg.Socket != "unix:path=/run/dbus/system_bus_socket" {
		t.Fatalf("socket address = %q", cc.cfg.Socket)
	}

	// A full D-Bus address in query is used verbatim.
	built, _ = Build(map[string]any{
		"bus": map[string]any{"type": "dbus", "query": "tcp:host=10.0.0.5,port=44444"},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(connCheck); cc.cfg.Socket != "tcp:host=10.0.0.5,port=44444" {
		t.Fatalf("query address = %q", cc.cfg.Socket)
	}
}

func TestBuildAvahiCheck(t *testing.T) {
	// No socket/query -> the system bus default address; default port 0; alias
	// avahi-daemon resolves.
	for _, typ := range []string{"avahi", "avahi-daemon"} {
		built, warns := Build(map[string]any{
			"mdns": map[string]any{"type": typ},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "avahi" || cc.cfg.Port != 0 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
		if cc.cfg.Socket != "unix:path=/var/run/dbus/system_bus_socket" {
			t.Fatalf("default address = %q", cc.cfg.Socket)
		}
	}

	// A full D-Bus address in query is used verbatim.
	built, _ := Build(map[string]any{
		"mdns": map[string]any{"type": "avahi", "query": "tcp:host=10.0.0.5,port=44444"},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(connCheck); cc.cfg.Socket != "tcp:host=10.0.0.5,port=44444" {
		t.Fatalf("query address = %q", cc.cfg.Socket)
	}
}

func TestBuildLibvirtCheck(t *testing.T) {
	// No socket and no host -> default to the local Unix socket; alias libvirtd
	// resolves; default TCP port 16509.
	for _, typ := range []string{"libvirt", "libvirtd"} {
		built, warns := Build(map[string]any{
			"vm": map[string]any{"type": typ},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "libvirt" || cc.cfg.Port != 16509 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
		if cc.cfg.Socket != "/var/run/libvirt/libvirt-sock" {
			t.Fatalf("%s should default to the local socket, got %q", typ, cc.cfg.Socket)
		}
	}

	// An explicit host selects TCP: no default socket is injected.
	built, warns := Build(map[string]any{
		"vm": map[string]any{"type": "libvirt", "host": "10.0.0.4", "query": "lxc:///"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("tcp libvirt should build: warns=%v", warns)
	}
	cc := built[0].Check.(connCheck)
	if cc.cfg.Socket != "" || cc.cfg.Host != "10.0.0.4" || cc.cfg.Query != "lxc:///" {
		t.Fatalf("cfg = %+v", cc.cfg)
	}

	// An explicit socket is kept verbatim.
	built, _ = Build(map[string]any{
		"vm": map[string]any{"type": "libvirt", "socket": "/run/libvirt/libvirt-sock"},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(connCheck); cc.cfg.Socket != "/run/libvirt/libvirt-sock" {
		t.Fatalf("cfg = %+v", cc.cfg)
	}
}

func TestBuildRspamdCheck(t *testing.T) {
	built, warns := Build(map[string]any{
		"spam": map[string]any{"type": "rspamd", "host": "127.0.0.1", "tls": "skip-verify"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("rspamd check should build: warns=%v", warns)
	}
	cc := built[0].Check.(connCheck)
	if cc.proto.Name() != "rspamd" || cc.cfg.Port != 11334 || cc.cfg.TLS != "skip-verify" {
		t.Fatalf("cfg = %+v", cc.cfg)
	}
}

func TestBuildDHCPCheck(t *testing.T) {
	// Broadcast form: interface + optional fixed MAC carried into Params; alias
	// dhcpd resolves; default port 67.
	for _, typ := range []string{"dhcp", "dhcpd"} {
		built, warns := Build(map[string]any{
			"leases": map[string]any{"type": typ, "interface": "eth0", "mac": "aa:bb:cc:dd:ee:ff"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "dhcp" || cc.cfg.Port != 67 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
		if len(cc.ifaces) != 1 || cc.ifaces[0] != "eth0" || cc.cfg.Params["mac"] != "aa:bb:cc:dd:ee:ff" {
			t.Fatalf("%s ifaces=%v params=%v", typ, cc.ifaces, cc.cfg.Params)
		}
	}

	// Unicast form: no interface, no MAC params.
	built, warns := Build(map[string]any{
		"leases": map[string]any{"type": "dhcp", "host": "10.0.0.1"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("unicast dhcp should build: warns=%v", warns)
	}
	if cc := built[0].Check.(connCheck); cc.cfg.Host != "10.0.0.1" || len(cc.cfg.Params) != 0 {
		t.Fatalf("cfg = %+v", cc.cfg)
	}

	// An invalid MAC fails the build with a clear message.
	_, warns = Build(map[string]any{
		"leases": map[string]any{"type": "dhcp", "interface": "eth0", "mac": "not-a-mac"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) == 0 || !strings.Contains(warns[0], "mac") {
		t.Fatalf("invalid mac should warn, got %v", warns)
	}
}

func TestBuildSMBCheck(t *testing.T) {
	// Anonymous (no user): builds, default port 445.
	for _, typ := range []string{"smb", "samba", "cifs"} {
		built, warns := Build(map[string]any{
			"share": map[string]any{"type": typ, "host": "127.0.0.1"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "smb" || cc.cfg.Port != 445 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
	}
	// With credentials + a share to verify.
	built, _ := Build(map[string]any{
		"share": map[string]any{"type": "smb", "host": "fs", "user": `WG\joe`, "password": "p", "query": "data"},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(connCheck); cc.cfg.User != `WG\joe` || cc.cfg.Query != "data" {
		t.Fatalf("cfg = %+v", cc.cfg)
	}
}

func TestBuildSpamdCheck(t *testing.T) {
	for _, typ := range []string{"spamd", "spamassassin"} {
		built, warns := Build(map[string]any{
			"sa": map[string]any{"type": typ, "host": "127.0.0.1"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "spamd" || cc.cfg.Port != 783 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
	}
	// Unix socket form.
	built, _ := Build(map[string]any{
		"sa": map[string]any{"type": "spamd", "socket": "/run/spamd.sock"},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(connCheck); cc.cfg.Socket != "/run/spamd.sock" {
		t.Fatalf("socket = %q", cc.cfg.Socket)
	}
}

func TestBuildClamdCheck(t *testing.T) {
	for _, typ := range []string{"clamd", "clamav"} {
		built, warns := Build(map[string]any{
			"av": map[string]any{"type": typ, "host": "127.0.0.1"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "clamd" || cc.cfg.Port != 3310 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
	}

	// Unix socket form, and the inherited on_version_change toggle.
	built, _ := Build(map[string]any{
		"av": map[string]any{"type": "clamd", "socket": "/run/clamav/clamd.ctl", "on_version_change": true},
	}, Deps{DefaultTimeout: time.Second})
	cc := built[0].Check.(connCheck)
	if cc.cfg.Socket != "/run/clamav/clamd.ctl" || !cc.onVersionChange {
		t.Fatalf("cfg = %+v onVersionChange=%v", cc.cfg, cc.onVersionChange)
	}
}

func TestBuildGlusterFSCheck(t *testing.T) {
	for _, typ := range []string{"glusterfs", "glusterd", "gluster"} {
		built, warns := Build(map[string]any{
			"gfs": map[string]any{"type": typ, "host": "127.0.0.1"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "glusterfs" || cc.cfg.Port != 24007 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
	}
}

func TestBuildCephCheck(t *testing.T) {
	for _, typ := range []string{"ceph", "ceph-mon"} {
		built, warns := Build(map[string]any{
			"mon": map[string]any{"type": typ, "host": "127.0.0.1"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "ceph" || cc.cfg.Port != 3300 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
	}
}

func TestBuildVarnishCheck(t *testing.T) {
	for _, typ := range []string{"varnish", "varnishadm"} {
		built, warns := Build(map[string]any{
			"cache": map[string]any{"type": typ, "host": "127.0.0.1"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "varnish" || cc.cfg.Port != 6082 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
	}
}

func TestBuildOpenvswitchCheck(t *testing.T) {
	for _, typ := range []string{"openvswitch", "ovs", "ovsdb", "ovsdb-server"} {
		built, warns := Build(map[string]any{
			"sw": map[string]any{"type": typ, "host": "127.0.0.1"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "openvswitch" || cc.cfg.Port != 6640 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
	}
	// Unix socket form.
	built, _ := Build(map[string]any{
		"sw": map[string]any{"type": "ovs", "socket": "/run/openvswitch/db.sock"},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(connCheck); cc.cfg.Socket != "/run/openvswitch/db.sock" {
		t.Fatalf("socket = %q", cc.cfg.Socket)
	}
}

func TestBuildMQTTCheck(t *testing.T) {
	built, warns := Build(map[string]any{
		"broker": map[string]any{"type": "mqtt", "host": "127.0.0.1"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("mqtt check should build: warns=%v", warns)
	}
	cc := built[0].Check.(connCheck)
	if cc.proto.Name() != "mqtt" || cc.cfg.Port != 1883 {
		t.Fatalf("cfg = %+v", cc.cfg)
	}
	// With credentials + MQTTS.
	built, _ = Build(map[string]any{
		"broker": map[string]any{"type": "mqtt", "port": 8883, "tls": true, "user": "u", "password": "p"},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(connCheck); cc.cfg.Port != 8883 || cc.cfg.User != "u" || cc.cfg.TLS != "true" {
		t.Fatalf("cfg = %+v", cc.cfg)
	}
}

func TestBuildSieveCheck(t *testing.T) {
	for _, typ := range []string{"sieve", "managesieve"} {
		built, warns := Build(map[string]any{
			"filter": map[string]any{"type": typ, "host": "127.0.0.1"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "sieve" || cc.cfg.Port != 4190 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
	}
}

func TestBuildAsteriskCheck(t *testing.T) {
	for _, typ := range []string{"asterisk", "ami"} {
		built, warns := Build(map[string]any{
			"pbx": map[string]any{"type": typ, "host": "127.0.0.1"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "asterisk" || cc.cfg.Port != 5038 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
	}
}

func TestBuildGuacdCheck(t *testing.T) {
	for _, typ := range []string{"guacd", "guacamole"} {
		built, warns := Build(map[string]any{
			"guac": map[string]any{"type": typ, "host": "127.0.0.1"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "guacd" || cc.cfg.Port != 4822 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
	}

	// query selects the Guacamole protocol to handshake with.
	built, _ := Build(map[string]any{
		"guac": map[string]any{"type": "guacd", "host": "127.0.0.1", "query": "rdp"},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(connCheck); cc.cfg.Query != "rdp" {
		t.Fatalf("query = %q, want rdp", cc.cfg.Query)
	}
}

func TestBuildRDPCheck(t *testing.T) {
	for _, typ := range []string{"rdp", "ms-wbt-server"} {
		built, warns := Build(map[string]any{
			"desktop": map[string]any{"type": typ, "host": "127.0.0.1"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "rdp" || cc.cfg.Port != 3389 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
	}
}

func TestBuildNFSCheck(t *testing.T) {
	for _, typ := range []string{"nfs", "nfs-server", "nfsd"} {
		built, warns := Build(map[string]any{
			"share": map[string]any{"type": typ, "host": "127.0.0.1"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "nfs" || cc.cfg.Port != 2049 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
	}
}

func TestBuildMountdCheck(t *testing.T) {
	for _, typ := range []string{"mountd", "rpc.mountd", "nfs-mountd"} {
		built, warns := Build(map[string]any{
			"mnt": map[string]any{"type": typ, "host": "127.0.0.1"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "mountd" || cc.cfg.Port != 20048 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
	}
	// An explicit port (mountd often runs on a configured/random port) is kept.
	built, _ := Build(map[string]any{
		"mnt": map[string]any{"type": "mountd", "host": "127.0.0.1", "port": 32767},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(connCheck); cc.cfg.Port != 32767 {
		t.Fatalf("port = %d", cc.cfg.Port)
	}
}

func TestBuildStatdCheck(t *testing.T) {
	for _, typ := range []string{"statd", "rpc.statd", "nsm", "nfs-statd"} {
		built, warns := Build(map[string]any{
			"sm": map[string]any{"type": typ, "host": "127.0.0.1"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "statd" || cc.cfg.Port != 662 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
	}
	// An explicit port (statd often runs on a configured/random port) is kept.
	built, _ := Build(map[string]any{
		"sm": map[string]any{"type": "statd", "host": "127.0.0.1", "port": 32765},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(connCheck); cc.cfg.Port != 32765 {
		t.Fatalf("port = %d", cc.cfg.Port)
	}
}

func TestBuildOpenVPNCheck(t *testing.T) {
	for _, typ := range []string{"openvpn", "ovpn"} {
		built, warns := Build(map[string]any{
			"vpn": map[string]any{"type": typ, "host": "10.0.0.1"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "openvpn" || cc.cfg.Port != 1194 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
		if cc.cfg.Params["transport"] != "" {
			t.Fatalf("default transport should be unset (udp), got %q", cc.cfg.Params["transport"])
		}
	}
	// transport: tcp is carried through params.
	built, _ := Build(map[string]any{
		"vpn": map[string]any{"type": "openvpn", "host": "10.0.0.1", "transport": "TCP"},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(connCheck); cc.cfg.Params["transport"] != "tcp" {
		t.Fatalf("transport = %q", cc.cfg.Params["transport"])
	}
	// An invalid transport is rejected.
	if _, warns := Build(map[string]any{
		"vpn": map[string]any{"type": "openvpn", "host": "10.0.0.1", "transport": "sctp"},
	}, Deps{DefaultTimeout: time.Second}); len(warns) == 0 {
		t.Fatal("an invalid transport must warn")
	}
}

func TestBuildNebulaCheck(t *testing.T) {
	for _, typ := range []string{"nebula", "nebula-vpn"} {
		built, warns := Build(map[string]any{
			"mesh": map[string]any{"type": typ, "host": "10.0.0.1"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "nebula" || cc.cfg.Port != 4242 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
	}
}

func TestBuildRpcbindCheck(t *testing.T) {
	for _, typ := range []string{"rpcbind", "portmap", "portmapper"} {
		built, warns := Build(map[string]any{
			"rpc": map[string]any{"type": typ, "host": "127.0.0.1"},
		}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 0 || len(built) != 1 {
			t.Fatalf("%s check should build: warns=%v", typ, warns)
		}
		cc := built[0].Check.(connCheck)
		if cc.proto.Name() != "rpcbind" || cc.cfg.Port != 111 {
			t.Fatalf("%s cfg = %+v", typ, cc.cfg)
		}
	}
}

func TestBuildFail2banCheck(t *testing.T) {
	// Socket-only: default port 0 and the well-known control socket by default.
	built, warns := Build(map[string]any{
		"f2b": map[string]any{"type": "fail2ban"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("fail2ban check should build: warns=%v", warns)
	}
	cc := built[0].Check.(connCheck)
	if cc.proto.Name() != "fail2ban" || cc.cfg.Port != 0 {
		t.Fatalf("cfg = %+v", cc.cfg)
	}
	if cc.cfg.Socket != "/var/run/fail2ban/fail2ban.sock" {
		t.Fatalf("default socket = %q", cc.cfg.Socket)
	}

	// An explicit socket is kept.
	built, _ = Build(map[string]any{
		"f2b": map[string]any{"type": "fail2ban", "socket": "/run/fail2ban/fail2ban.sock"},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(connCheck); cc.cfg.Socket != "/run/fail2ban/fail2ban.sock" {
		t.Fatalf("socket = %q", cc.cfg.Socket)
	}
}

func TestBuildLvmpolldCheck(t *testing.T) {
	// Socket-only: default port 0 and the well-known control socket by default.
	built, warns := Build(map[string]any{
		"lvm": map[string]any{"type": "lvmpolld"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("lvmpolld check should build: warns=%v", warns)
	}
	cc := built[0].Check.(connCheck)
	if cc.proto.Name() != "lvmpolld" || cc.cfg.Port != 0 {
		t.Fatalf("cfg = %+v", cc.cfg)
	}
	if cc.cfg.Socket != "/run/lvm/lvmpolld.socket" {
		t.Fatalf("default socket = %q", cc.cfg.Socket)
	}

	// An explicit socket is kept.
	built, _ = Build(map[string]any{
		"lvm": map[string]any{"type": "lvmpolld", "socket": "/var/run/lvm/lvmpolld.socket"},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(connCheck); cc.cfg.Socket != "/var/run/lvm/lvmpolld.socket" {
		t.Fatalf("socket = %q", cc.cfg.Socket)
	}
}

func TestBuildAcpidCheck(t *testing.T) {
	// Socket-only: default port 0 and the well-known socket when none is given.
	built, warns := Build(map[string]any{
		"acpi": map[string]any{"type": "acpid"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("acpid check should build: warns=%v", warns)
	}
	cc := built[0].Check.(connCheck)
	if cc.proto.Name() != "acpid" || cc.cfg.Port != 0 {
		t.Fatalf("cfg = %+v", cc.cfg)
	}
	if cc.cfg.Socket != "/var/run/acpid.socket" {
		t.Fatalf("default socket = %q", cc.cfg.Socket)
	}

	// An explicit socket is kept.
	built, _ = Build(map[string]any{
		"acpi": map[string]any{"type": "acpid", "socket": "/run/acpid.socket"},
	}, Deps{DefaultTimeout: time.Second})
	if cc := built[0].Check.(connCheck); cc.cfg.Socket != "/run/acpid.socket" {
		t.Fatalf("socket = %q", cc.cfg.Socket)
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
