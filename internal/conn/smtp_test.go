package conn

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestSMTPRegistered(t *testing.T) {
	p, ok := Lookup("smtp")
	if !ok {
		t.Fatal("smtp not registered")
	}
	if p.DefaultPort() != 25 {
		t.Fatalf("default port = %d, want 25", p.DefaultPort())
	}
	if p.RequiresUser() {
		t.Fatal("smtp must not require a user (anonymous check allowed)")
	}
}

func TestSMTPHandshakeAnonymous(t *testing.T) {
	replies := "220 mail.example ESMTP Postfix\r\n" +
		"250-mail.example\r\n250-PIPELINING\r\n250 AUTH PLAIN LOGIN\r\n"
	conn := rw{in: strings.NewReader(replies), out: &bytes.Buffer{}}
	res, err := smtpHandshake(conn, Config{})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	sent := conn.out.String()
	if !strings.Contains(sent, "EHLO") {
		t.Fatalf("must send EHLO: %q", sent)
	}
	if strings.Contains(sent, "AUTH") {
		t.Fatalf("anonymous check must not AUTH: %q", sent)
	}
	if !strings.Contains(res.Extra["greeting"], "Postfix") {
		t.Fatalf("greeting not captured: %v", res.Extra)
	}
}

func TestSMTPHandshakeAuthPlain(t *testing.T) {
	replies := "220 ready\r\n250 mail\r\n235 2.7.0 Authentication successful\r\n"
	conn := rw{in: strings.NewReader(replies), out: &bytes.Buffer{}}
	if _, err := smtpHandshake(conn, Config{User: "joe", Password: "secret"}); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	sent := conn.out.String()
	// Extract the AUTH PLAIN argument and verify it decodes to \0user\0pass.
	idx := strings.Index(sent, "AUTH PLAIN ")
	if idx < 0 {
		t.Fatalf("AUTH PLAIN not sent: %q", sent)
	}
	arg := strings.SplitN(sent[idx+len("AUTH PLAIN "):], "\r\n", 2)[0]
	raw, err := base64.StdEncoding.DecodeString(arg)
	if err != nil {
		t.Fatalf("auth arg not base64: %q", arg)
	}
	if string(raw) != "\x00joe\x00secret" {
		t.Fatalf("auth payload = %q, want \\x00joe\\x00secret", raw)
	}
}

func TestSMTPHandshakeAuthFails(t *testing.T) {
	replies := "220 ready\r\n250 mail\r\n535 5.7.8 authentication failed\r\n"
	conn := rw{in: strings.NewReader(replies), out: &bytes.Buffer{}}
	if _, err := smtpHandshake(conn, Config{User: "joe", Password: "bad"}); err == nil {
		t.Fatal("a 535 reply must fail")
	}
}

func TestSMTPHandshakeBadGreeting(t *testing.T) {
	conn := rw{in: strings.NewReader("421 service not available\r\n"), out: &bytes.Buffer{}}
	if _, err := smtpHandshake(conn, Config{}); err == nil {
		t.Fatal("a non-220 greeting must fail")
	}
}

func TestSMTPHandshakeHELOFallback(t *testing.T) {
	// EHLO not implemented (502) -> fall back to HELO.
	replies := "220 ready\r\n502 command not implemented\r\n250 mail\r\n"
	conn := rw{in: strings.NewReader(replies), out: &bytes.Buffer{}}
	if _, err := smtpHandshake(conn, Config{}); err != nil {
		t.Fatalf("HELO fallback should succeed: %v", err)
	}
	if !strings.Contains(conn.out.String(), "HELO ") {
		t.Fatalf("must fall back to HELO: %q", conn.out.String())
	}
}

func TestReadReplyCodeRejectsMismatchedMultilineTerminator(t *testing.T) {
	br := bufio.NewReader(strings.NewReader("220-Welcome\r\n421 service closing\r\n"))
	if _, _, err := readReplyCode(br); err == nil {
		t.Fatal("mismatched final code in a multi-line reply must fail")
	}
}
