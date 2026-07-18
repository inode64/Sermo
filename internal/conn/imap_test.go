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
	// The quote in the password must arrive escaped inside the quoted string.
	assertHandshakeLogin(t, imapHandshake, replies, Config{User: "joe@x.com", Password: "p\"ss"},
		"LOGIN", "joe@x.com", `"p\"ss"`)
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
