package conn

import (
	"bytes"
	"strings"
	"testing"
)

func TestPOPHandshakeAnonymous(t *testing.T) {
	assertHandshakeAnonymous(t, popHandshake, "+OK POP3 server ready\r\n", "USER", "POP3 server ready")
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
	assertHandshakeFails(t, popHandshake, replies, Config{User: "joe", Password: "bad"})
}

func TestPOPHandshakeBadGreeting(t *testing.T) {
	assertHandshakeFails(t, popHandshake, "-ERR server unavailable\r\n", Config{})
}
