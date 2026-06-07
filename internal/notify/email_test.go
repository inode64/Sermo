package notify

import (
	"context"
	"strings"
	"testing"
)

func TestParseEmailDSN(t *testing.T) {
	cases := []struct {
		dsn         string
		host, port  string
		user, pass  string
		implicitTLS bool
		wantErr     bool
	}{
		{dsn: "smtp://smtp.example.com", host: "smtp.example.com", port: "587"},
		{dsn: "smtps://smtp.example.com", host: "smtp.example.com", port: "465", implicitTLS: true},
		{dsn: "smtp://user:pass@mail.example.com:2525", host: "mail.example.com", port: "2525", user: "user", pass: "pass"},
		{dsn: "ftp://x", wantErr: true},
		{dsn: "smtp://", wantErr: true},
	}
	for _, tc := range cases {
		d, err := parseEmailDSN(tc.dsn)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: expected error", tc.dsn)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.dsn, err)
		}
		if d.host != tc.host || d.port != tc.port || d.user != tc.user || d.pass != tc.pass || d.implicitTLS != tc.implicitTLS {
			t.Fatalf("%s: got %+v", tc.dsn, d)
		}
	}
}

func TestBuildEmailRequiresFields(t *testing.T) {
	good, err := buildEmail("ops", map[string]any{
		"type": "email", "dsn": "smtp://localhost:25", "from": "sermo@x", "to": []any{"a@x", "b@x"},
	})
	if err != nil {
		t.Fatalf("valid email: %v", err)
	}
	if e := good.(*Email); len(e.to) != 2 || e.Type() != "email" || e.Name() != "ops" {
		t.Fatalf("unexpected email: %+v", e)
	}

	for _, entry := range []map[string]any{
		{"type": "email", "from": "x@y", "to": []any{"a@b"}},                            // no dsn
		{"type": "email", "dsn": "smtp://x", "to": []any{"a@b"}},                        // no from
		{"type": "email", "dsn": "smtp://x", "from": "x@y"},                             // no to
		{"type": "email", "dsn": "carrier-pigeon://x", "from": "x", "to": []any{"a@b"}}, // bad dsn
	} {
		if _, err := buildEmail("n", entry); err == nil {
			t.Fatalf("expected error for %v", entry)
		}
	}
}

func TestEmailSendDispatchesToSender(t *testing.T) {
	var gotFrom string
	var gotTo []string
	var gotMsg Message
	e := &Email{
		name: "ops", from: "Sermo <sermo@x>", to: []string{"a@x", "b@x"},
		dsn: emailDSN{host: "h", port: "25"},
		send: func(_ context.Context, _ emailDSN, from string, to []string, msg Message) error {
			gotFrom, gotTo, gotMsg = from, to, msg
			return nil
		},
	}
	if err := e.Send(context.Background(), Message{Subject: "s", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if gotFrom != "Sermo <sermo@x>" || len(gotTo) != 2 || gotMsg.Subject != "s" {
		t.Fatalf("sender got from=%q to=%v msg=%+v", gotFrom, gotTo, gotMsg)
	}
}

func TestBuildMessageHeadersAndInjectionGuard(t *testing.T) {
	raw := string(buildMessage("sermo@x", []string{"a@x", "b@x"}, Message{
		Subject: "alert\r\nBcc: evil@x", // header-injection attempt
		Body:    "line1\nline2",
	}))
	if !strings.Contains(raw, "From: sermo@x\r\n") || !strings.Contains(raw, "To: a@x, b@x\r\n") {
		t.Fatalf("missing headers:\n%s", raw)
	}
	// The CRLF in the subject must not have spawned a new header line.
	if strings.Contains(raw, "\nBcc:") || strings.Count(raw, "Subject:") != 1 {
		t.Fatalf("subject not sanitized (header injection):\n%s", raw)
	}
	if !strings.Contains(raw, "line1\r\nline2") {
		t.Fatalf("body not CRLF-normalized:\n%s", raw)
	}
}

func TestBareAddr(t *testing.T) {
	if got := bareAddr("Sermo Ops <ops@example.com>"); got != "ops@example.com" {
		t.Fatalf("bareAddr = %q", got)
	}
	if got := bareAddr("plain@example.com"); got != "plain@example.com" {
		t.Fatalf("bareAddr = %q", got)
	}
}
