package checks

import (
	"context"
	"testing"
	"time"

	"sermo/internal/conn"
)

// probeSeq returns a probe that yields results[0], results[1], … on successive
// calls (repeating the last once exhausted).
func probeSeq(results ...conn.Result) func(context.Context, conn.Config) (conn.Result, error) {
	i := 0
	return func(context.Context, conn.Config) (conn.Result, error) {
		r := results[i]
		if i < len(results)-1 {
			i++
		}
		return r, nil
	}
}

func TestConnVersionChange(t *testing.T) {
	// A protocol that reports a version (e.g. redis/mysql).
	c := connCheck{
		base:            base{name: "db", timeout: time.Second},
		proto:           fakeProto{},
		cfg:             conn.Config{Host: "h", Port: 1},
		onVersionChange: true,
		state:           &connState{},
		probe:           probeSeq(conn.Result{Version: "8.0.36"}, conn.Result{Version: "8.0.36"}, conn.Result{Version: "8.4.0"}),
	}
	// First cycle primes, no alert.
	if r := c.Run(context.Background()); !r.OK {
		t.Fatalf("first cycle must prime, not alert: %s", r.Message)
	}
	// Same version: still ok.
	if r := c.Run(context.Background()); !r.OK {
		t.Fatalf("unchanged version must stay ok: %s", r.Message)
	}
	// Version changes: alert (fails) and exposes old/new.
	r := c.Run(context.Background())
	if r.OK {
		t.Fatal("a changed version must fail the check")
	}
	if r.Data["version_old"] != "8.0.36" || r.Data["version"] != "8.4.0" {
		t.Fatalf("data should carry old/new version: %v", r.Data)
	}
	// After alerting once, the new value is the baseline (no repeat alert).
	if r := c.Run(context.Background()); !r.OK {
		t.Fatalf("must not keep alerting on a stable version: %s", r.Message)
	}
}

func TestConnVersionChangeFromGreeting(t *testing.T) {
	// smtp/imap/pop/ftp report no Version, only a greeting banner — version
	// identity falls back to that banner.
	greet := func(b string) conn.Result { return conn.Result{Extra: map[string]string{"greeting": b}} }
	c := connCheck{
		base:            base{name: "mail", timeout: time.Second},
		proto:           fakeProto{},
		cfg:             conn.Config{Host: "h", Port: 25},
		onVersionChange: true,
		state:           &connState{},
		probe:           probeSeq(greet("mail ESMTP Postfix 3.6"), greet("mail ESMTP Postfix 3.8")),
	}
	if r := c.Run(context.Background()); !r.OK {
		t.Fatalf("first cycle must prime: %s", r.Message)
	}
	r := c.Run(context.Background())
	if r.OK {
		t.Fatal("a changed greeting banner must alert")
	}
	if r.Data["version_old"] != "mail ESMTP Postfix 3.6" {
		t.Fatalf("data = %v", r.Data)
	}
}

func TestConnChangeAndVersionTogether(t *testing.T) {
	// on_change (fingerprint) and on_version_change can both be active.
	c := connCheck{
		base:            base{name: "ssh", timeout: time.Second},
		proto:           fakeProto{},
		cfg:             conn.Config{Host: "h", Port: 22},
		onChange:        true,
		onVersionChange: true,
		state:           &connState{},
		probe: probeSeq(
			conn.Result{Version: "OpenSSH_9.6", Extra: map[string]string{"fingerprint": "AAA"}},
			conn.Result{Version: "OpenSSH_9.6", Extra: map[string]string{"fingerprint": "BBB"}},
		),
	}
	if r := c.Run(context.Background()); !r.OK {
		t.Fatalf("prime cycle: %s", r.Message)
	}
	if r := c.Run(context.Background()); r.OK {
		t.Fatal("a fingerprint change must still alert when version is unchanged")
	}
}

func TestBuildConnCheckOnVersionChange(t *testing.T) {
	built, warns := Build(map[string]any{
		"mail": map[string]any{"type": "smtp", "host": "mail.example", "on_version_change": true},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("smtp with on_version_change should build: warns=%v", warns)
	}
	cc := built[0].Check.(connCheck)
	if !cc.onVersionChange || cc.state == nil {
		t.Fatal("on_version_change must enable stateful version detection")
	}
}
