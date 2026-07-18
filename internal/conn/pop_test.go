package conn

import (
	"testing"
)

func TestPOPHandshakeAnonymous(t *testing.T) {
	assertHandshakeAnonymous(t, popHandshake, "+OK POP3 server ready\r\n", "USER", "POP3 server ready")
}

func TestPOPHandshakeLogin(t *testing.T) {
	replies := "+OK ready\r\n" + "+OK user accepted\r\n" + "+OK mailbox locked and ready\r\n"
	assertHandshakeLogin(t, popHandshake, replies, Config{User: "joe", Password: "secret"}, "USER joe", "PASS secret")
}

func TestPOPHandshakeLoginFails(t *testing.T) {
	replies := "+OK ready\r\n" + "+OK\r\n" + "-ERR invalid password\r\n"
	assertHandshakeFails(t, popHandshake, replies, Config{User: "joe", Password: "bad"})
}

func TestPOPHandshakeBadGreeting(t *testing.T) {
	assertHandshakeFails(t, popHandshake, "-ERR server unavailable\r\n", Config{})
}
