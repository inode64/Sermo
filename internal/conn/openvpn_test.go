package conn

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"testing"
)

func TestOpenVPNClientReset(t *testing.T) {
	sid := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	b := openvpnClientReset(sid)
	if len(b) != 14 {
		t.Fatalf("len = %d, want 14", len(b))
	}
	if b[0] != 0x38 { // opcode 7 (high 5 bits), key id 0
		t.Fatalf("byte0 = %#x, want 0x38", b[0])
	}
	if !bytes.Equal(b[1:9], sid) || b[9] != 0 {
		t.Fatalf("packet = % x", b)
	}
}

// openvpnServerReply builds a P_CONTROL_HARD_RESET_SERVER_V2 acknowledging the
// client's reset (ack length 1, echoing the client session id).
func openvpnServerReply(clientSID []byte) []byte {
	b := []byte{openvpnHardResetServerV2 << 3}                    // opcode 8, key id 0
	b = append(b, 0xA0, 0xA1, 0xA2, 0xA3, 0xA4, 0xA5, 0xA6, 0xA7) // server session id
	b = append(b, 0x01)                                           // ACK array length = 1
	b = append(b, 0, 0, 0, 0)                                     // acked packet-id = 0
	b = append(b, clientSID...)                                   // remote session id = client's
	b = append(b, 0, 0, 0, 0)                                     // server message packet-id = 0
	return b
}

func TestParseOpenVPNReset(t *testing.T) {
	sid := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	if err := parseOpenVPNReset(openvpnServerReply(sid), sid); err != nil {
		t.Fatalf("valid reply: %v", err)
	}
	// Wrong opcode (echo our own client reset back).
	if err := parseOpenVPNReset(openvpnClientReset(sid), sid); err == nil {
		t.Fatal("a non-hard_reset_server reply must error")
	}
	// Session id mismatch.
	if err := parseOpenVPNReset(openvpnServerReply(sid), []byte{9, 9, 9, 9, 9, 9, 9, 9}); err == nil {
		t.Fatal("a session-id mismatch must error")
	}
	// No acknowledgement (ack length 0).
	noack := []byte{openvpnHardResetServerV2 << 3, 0, 0, 0, 0, 0, 0, 0, 0, 0x00, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if err := parseOpenVPNReset(noack, sid); err == nil {
		t.Fatal("a reply that does not acknowledge the reset must error")
	}
	// Short.
	if err := parseOpenVPNReset(openvpnServerReply(sid)[:12], sid); err == nil {
		t.Fatal("a short reply must error")
	}
}

func TestOpenVPNProbeUDP(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pc.Close() }()
	go func() {
		buf := make([]byte, 64)
		n, addr, err := pc.ReadFrom(buf)
		if err != nil || n < 9 {
			return
		}
		_, _ = pc.WriteTo(openvpnServerReply(buf[1:9]), addr)
	}()

	_, portStr, _ := net.SplitHostPort(pc.LocalAddr().String())
	port, _ := strconv.Atoi(portStr)
	res, err := openvpnProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["transport"] != "udp" || res.Extra["reply"] != "hard_reset_server" {
		t.Fatalf("extra = %v", res.Extra)
	}
}

func TestOpenVPNProbeTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		var lb [2]byte
		if _, err := io.ReadFull(c, lb[:]); err != nil {
			return
		}
		req := make([]byte, int(binary.BigEndian.Uint16(lb[:])))
		if _, err := io.ReadFull(c, req); err != nil || len(req) < 9 {
			return
		}
		reply := openvpnServerReply(req[1:9])
		out := make([]byte, 2+len(reply))
		binary.BigEndian.PutUint16(out[:2], uint16(len(reply)))
		copy(out[2:], reply)
		_, _ = c.Write(out)
	}()

	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	res, err := openvpnProtocol{}.Probe(context.Background(), Config{
		Host: "127.0.0.1", Port: port, Params: map[string]string{"transport": "tcp"},
	})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["transport"] != "tcp" {
		t.Fatalf("transport = %q", res.Extra["transport"])
	}
}
