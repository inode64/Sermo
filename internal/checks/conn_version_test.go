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
		name: "db", timeout: time.Second,
		proto:           fakeProto{},
		cfg:             conn.Config{Host: "h", Port: 1},
		onVersionChange: true,
		state:           &connState{},
		probe:           probeSeq(conn.Result{Version: "\n8.0.36\n"}, conn.Result{Version: "8.0.36"}, conn.Result{Version: "\n8.4.0\n"}),
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
		name: "mail", timeout: time.Second,
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

// A banner can carry the time of the connection (Exim, Sendmail) or a per-session
// APOP timestamp. Neither says anything about the software that answered.
func TestConnVersionChangeIgnoresPerConnectionGreetingText(t *testing.T) {
	tests := []struct {
		name     string
		first    string
		second   string
		upgraded string
	}{
		{
			name:     "exim",
			first:    "mx.example.com ESMTP Exim 4.98 Sun, 01 Nov 2026 12:00:00 +0100",
			second:   "mx.example.com ESMTP Exim 4.98 Sun, 01 Nov 2026 12:00:30 +0100",
			upgraded: "mx.example.com ESMTP Exim 4.99 Sun, 01 Nov 2026 12:01:00 +0100",
		},
		{
			name:     "sendmail",
			first:    "mx.example.com ESMTP Sendmail 8.18.1/8.18.1; Sun, 1 Nov 2026 12:00:00 +0100",
			second:   "mx.example.com ESMTP Sendmail 8.18.1/8.18.1; Mon, 2 Nov 2026 00:00:30 +0100",
			upgraded: "mx.example.com ESMTP Sendmail 8.18.2/8.18.2; Mon, 2 Nov 2026 00:01:00 +0100",
		},
		{
			name:     "date with a zone comment",
			first:    "mx.example.com IMAP4rev1 2007f.404 at Sun, 1 Nov 2026 12:00:00 +0100 (CET)",
			second:   "mx.example.com IMAP4rev1 2007f.404 at Sun, 1 Nov 2026 12:00:30 +0100 (CET)",
			upgraded: "mx.example.com IMAP4rev1 2007g.404 at Sun, 1 Nov 2026 12:01:00 +0100 (CET)",
		},
		{
			name:     "postfix",
			first:    "mx.example.com ESMTP Postfix (Debian/GNU)",
			second:   "mx.example.com ESMTP Postfix (Debian/GNU)",
			upgraded: "mx.example.com ESMTP Postfix 3.8",
		},
		{
			name:     "apop",
			first:    "Dovecot ready. <1896.697170952@mx.example.com>",
			second:   "Dovecot ready. <1897.697170982@mx.example.com>",
			upgraded: "Courier-POP3 ready. <1898.697171012@mx.example.com>",
		},
	}
	greet := func(b string) conn.Result { return conn.Result{Extra: map[string]string{conn.ExtraKeyGreeting: b}} }
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := connCheck{
				name: "mail", timeout: time.Second,
				proto:           fakeProto{},
				cfg:             conn.Config{Host: "h", Port: 25},
				onVersionChange: true,
				state:           &connState{},
				probe:           probeSeq(greet(test.first), greet(test.second), greet(test.upgraded)),
			}
			if r := c.Run(context.Background()); !r.OK {
				t.Fatalf("first cycle must prime: %s", r.Message)
			}
			r := c.Run(context.Background())
			if !r.OK {
				t.Fatalf("the same software must stay ok: %s", r.Message)
			}
			if r.Data[conn.ExtraKeyGreeting] != test.second {
				t.Fatalf("greeting data = %v, want it as the server sent it", r.Data[conn.ExtraKeyGreeting])
			}
			if r := c.Run(context.Background()); r.OK {
				t.Fatal("a changed version must still alert")
			}
		})
	}
}

func TestConnChangeAndVersionTogether(t *testing.T) {
	// on_change (fingerprint) and on_version_change can both be active.
	c := connCheck{
		name: "ssh", timeout: time.Second,
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
