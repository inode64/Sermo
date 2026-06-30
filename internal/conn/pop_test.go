package conn

import (
	"bytes"
	"strings"
	"testing"
)

func TestPOPHandshakeAnonymous(t *testing.T) {
	conn := rw{in: strings.NewReader("+OK POP3 server ready\r\n"), out: &bytes.Buffer{}}
	res, err := popHandshake(conn, Config{})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if strings.Contains(conn.out.String(), "USER") {
		t.Fatalf("anonymous check must not send USER: %q", conn.out.String())
	}
	if !strings.Contains(res.Extra["greeting"], "POP3 server ready") {
		t.Fatalf("greeting not captured: %v", res.Extra)
	}
}

func TestPOPHandshakeLogin(t *testing.T) {
	replies := "+OK ready\r\n" + "+OK user accepted\r\n" + "+OK mailbox locked and ready\r\n"
	conn := rw{in: strings.NewReader(replies), out: &bytes.Buffer{}}
	if _, err := popHandshake(conn, Config{User: "joe", Password: "secret"}); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	sent := conn.out.String()
	if !strings.Contains(sent, "USER joe") || !strings.Contains(sent, "PASS secret") {
		t.Fatalf("USER/PASS not sent: %q", sent)
	}
}

func TestPOPHandshakeLoginFails(t *testing.T) {
	replies := "+OK ready\r\n" + "+OK\r\n" + "-ERR invalid password\r\n"
	conn := rw{in: strings.NewReader(replies), out: &bytes.Buffer{}}
	if _, err := popHandshake(conn, Config{User: "joe", Password: "bad"}); err == nil {
		t.Fatal("a -ERR PASS reply must fail")
	}
}

func TestPOPHandshakeBadGreeting(t *testing.T) {
	conn := rw{in: strings.NewReader("-ERR server unavailable\r\n"), out: &bytes.Buffer{}}
	if _, err := popHandshake(conn, Config{}); err == nil {
		t.Fatal("a -ERR greeting must fail")
	}
}
