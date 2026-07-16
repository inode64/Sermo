package conn

import (
	"bytes"
	"strings"
	"testing"
)

func TestIMAPHandshakeAnonymous(t *testing.T) {
	// Anonymous: only the greeting is read; no LOGIN is sent.
	assertHandshakeAnonymous(t, imapHandshake, "* OK [CAPABILITY IMAP4rev1] Dovecot ready.\r\n", "LOGIN", "Dovecot ready")
}

func TestIMAPHandshakeLogin(t *testing.T) {
	replies := "* OK ready\r\n" + "a1 OK [CAPABILITY IMAP4rev1] Logged in\r\n"
	conn := rw{in: strings.NewReader(replies), out: &bytes.Buffer{}}
	if _, err := imapHandshake(conn, Config{User: "joe@x.com", Password: "p\"ss"}); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	sent := conn.out.String()
	if !strings.Contains(sent, "LOGIN") || !strings.Contains(sent, "joe@x.com") {
		t.Fatalf("login not sent: %q", sent)
	}
	// Password with a quote must be escaped inside the quoted string.
	if !strings.Contains(sent, `"p\"ss"`) {
		t.Fatalf("password not quoted/escaped: %q", sent)
	}
}

func TestIMAPHandshakeLoginFails(t *testing.T) {
	replies := "* OK ready\r\n" + "a1 NO [AUTHENTICATIONFAILED] Invalid credentials\r\n"
	assertHandshakeFails(t, imapHandshake, replies, Config{User: "u", Password: "bad"})
}

func TestIMAPHandshakeBadGreeting(t *testing.T) {
	assertHandshakeFails(t, imapHandshake, "* BYE Too many connections\r\n", Config{})
}

func TestIMAPHandshakePreauth(t *testing.T) {
	// PREAUTH greeting means already authenticated; no LOGIN needed even with creds.
	conn := rw{in: strings.NewReader("* PREAUTH IMAP4rev1 server ready\r\n"), out: &bytes.Buffer{}}
	if _, err := imapHandshake(conn, Config{User: "u", Password: "p"}); err != nil {
		t.Fatalf("preauth greeting should pass: %v", err)
	}
	if strings.Contains(conn.out.String(), "LOGIN") {
		t.Fatalf("PREAUTH must skip LOGIN: %q", conn.out.String())
	}
}
