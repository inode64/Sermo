package conn

import (
	"bytes"
	"strings"
	"testing"
)

func TestFTPRegistered(t *testing.T) {
	p, ok := Lookup("ftp")
	if !ok {
		t.Fatal("ftp not registered")
	}
	if p.DefaultPort() != 21 {
		t.Fatalf("default port = %d, want 21", p.DefaultPort())
	}
	if p.RequiresUser() {
		t.Fatal("ftp must not require a user (anonymous check allowed)")
	}
}

func TestFTPHandshakeAnonymous(t *testing.T) {
	conn := rw{in: strings.NewReader("220-Welcome\r\n220 ProFTPD 1.3 ready\r\n"), out: &bytes.Buffer{}}
	res, err := ftpHandshake(conn, Config{})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if strings.Contains(conn.out.String(), "USER") {
		t.Fatalf("anonymous check must not send USER: %q", conn.out.String())
	}
	if !strings.Contains(res.Extra["greeting"], "ProFTPD") {
		t.Fatalf("greeting (multi-line) not captured: %v", res.Extra)
	}
}

func TestFTPHandshakeLogin(t *testing.T) {
	replies := "220 ready\r\n" + "331 password required\r\n" + "230 user logged in\r\n"
	conn := rw{in: strings.NewReader(replies), out: &bytes.Buffer{}}
	if _, err := ftpHandshake(conn, Config{User: "joe", Password: "secret"}); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	sent := conn.out.String()
	if !strings.Contains(sent, "USER joe") || !strings.Contains(sent, "PASS secret") {
		t.Fatalf("USER/PASS not sent: %q", sent)
	}
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
	conn := rw{in: strings.NewReader(replies), out: &bytes.Buffer{}}
	if _, err := ftpHandshake(conn, Config{User: "joe", Password: "bad"}); err == nil {
		t.Fatal("a 530 reply must fail")
	}
}

func TestFTPHandshakeBadGreeting(t *testing.T) {
	conn := rw{in: strings.NewReader("421 service not available\r\n"), out: &bytes.Buffer{}}
	if _, err := ftpHandshake(conn, Config{}); err == nil {
		t.Fatal("a non-220 greeting must fail")
	}
}
