package conn

import (
	"bytes"
	"strings"
	"testing"
)

func TestFTPHandshakeAnonymous(t *testing.T) {
	assertHandshakeAnonymous(t, ftpHandshake, "220-Welcome\r\n220 ProFTPD 1.3 ready\r\n", "USER", "ProFTPD")
}

func TestFTPHandshakeLogin(t *testing.T) {
	replies := "220 ready\r\n" + "331 password required\r\n" + "230 user logged in\r\n"
	assertHandshakeLogin(t, ftpHandshake, replies, Config{User: "joe", Password: "secret"}, "USER joe", "PASS secret")
}

func TestFTPHandshakeUserNoPassword(t *testing.T) {
	// Server accepts the user without a password (230 straight after USER).
	replies := "220 ready\r\n" + "230 logged in\r\n"
	conn := rw{in: strings.NewReader(replies), out: &bytes.Buffer{}}
	if _, err := ftpHandshake(conn, Config{User: "guest"}); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if strings.Contains(conn.out.String(), "PASS") {
		t.Fatalf("must not send PASS when USER already returned 230: %q", conn.out.String())
	}
}

func TestFTPHandshakePasswordOnlyIsAnonymous(t *testing.T) {
	replies := "220 ready\r\n" + "331 need password\r\n" + "230 ok\r\n"
	conn := rw{in: strings.NewReader(replies), out: &bytes.Buffer{}}
	if _, err := ftpHandshake(conn, Config{Password: "me@example.com"}); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if !strings.Contains(conn.out.String(), "USER anonymous") {
		t.Fatalf("a password without a user should log in as anonymous: %q", conn.out.String())
	}
}

func TestFTPHandshakeLoginFails(t *testing.T) {
	replies := "220 ready\r\n" + "331 need password\r\n" + "530 login incorrect\r\n"
	assertHandshakeFails(t, ftpHandshake, replies, Config{User: "joe", Password: "bad"})
}

func TestFTPHandshakeBadGreeting(t *testing.T) {
	assertHandshakeFails(t, ftpHandshake, "421 service not available\r\n", Config{})
}
