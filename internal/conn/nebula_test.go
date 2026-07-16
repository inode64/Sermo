package conn

import (
	"context"
	"encoding/binary"
	"testing"
)

func TestNebulaMessage(t *testing.T) {
	b := nebulaMessage(0xdeadbeef)
	if len(b) != 16 {
		t.Fatalf("len = %d, want 16", len(b))
	}
	if b[0] != 0x11 { // version 1 (high nibble), Message type 1 (low nibble)
		t.Fatalf("byte0 = %#x, want 0x11", b[0])
	}
	if binary.BigEndian.Uint32(b[4:8]) != 0xdeadbeef {
		t.Fatalf("index = %#x", binary.BigEndian.Uint32(b[4:8]))
	}
}

func TestParseNebulaRecvError(t *testing.T) {
	good := make([]byte, 16)
	good[0] = 0x12 // version 1, RecvError type 2
	binary.BigEndian.PutUint32(good[4:8], 0x01020304)
	if err := parseNebulaRecvError(good, 0x01020304); err != nil {
		t.Fatalf("valid recv_error: %v", err)
	}
	// Wrong type (a Message, not a recv_error).
	msg := nebulaMessage(0x01020304)
	if err := parseNebulaRecvError(msg, 0x01020304); err == nil {
		t.Fatal("a non-recv_error reply must error")
	}
	// Index mismatch.
	if err := parseNebulaRecvError(good, 0x99999999); err == nil {
		t.Fatal("an index mismatch must error")
	}
	// Short.
	if err := parseNebulaRecvError(good[:8], 0x01020304); err == nil {
		t.Fatal("a short reply must error")
	}
}

// A node that has no tunnel for the probed index replies with a recv_error
// header echoing that index — the default ("always") behaviour.
func TestNebulaProbeAgainstFakeNode(t *testing.T) {
	port := serveUDPOnce(t, func(req []byte) []byte {
		if len(req) < 16 {
			return nil
		}
		// Echo a recv_error: type 2, same index as the incoming Message.
		reply := make([]byte, 16)
		reply[0] = 0x12
		copy(reply[4:8], req[4:8])
		return reply
	})
	res, err := nebulaProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["reply"] != "recv_error" {
		t.Fatalf("reply = %q", res.Extra["reply"])
	}
}
