package conn

import (
	"context"
	"encoding/binary"
	"net"
	"strconv"
	"testing"
)

func TestRDPRegistered(t *testing.T) {
	for _, name := range []string{"rdp", "ms-wbt-server"} {
		p, ok := Lookup(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if p.DefaultPort() != 3389 {
			t.Fatalf("%s default port = %d, want 3389", name, p.DefaultPort())
		}
		if p.RequiresUser() {
			t.Fatalf("%s must not require a user", name)
		}
	}
}

func TestBuildRDPNegRequest(t *testing.T) {
	b := buildRDPNegRequest(rdpRequestedProtocols)
	if len(b) != 19 {
		t.Fatalf("len = %d, want 19", len(b))
	}
	if b[0] != 0x03 { // TPKT version
		t.Fatalf("tpkt version = 0x%02x", b[0])
	}
	if int(binary.BigEndian.Uint16(b[2:4])) != 19 {
		t.Fatalf("tpkt length = %d, want 19", binary.BigEndian.Uint16(b[2:4]))
	}
	if b[5] != 0xE0 { // X.224 CR
		t.Fatalf("x224 code = 0x%02x, want 0xE0", b[5])
	}
	if b[11] != 0x01 { // RDP_NEG_REQ type
		t.Fatalf("neg type = 0x%02x, want 0x01", b[11])
	}
	if binary.LittleEndian.Uint32(b[15:19]) != rdpRequestedProtocols {
		t.Fatalf("requested protocols = %#x", binary.LittleEndian.Uint32(b[15:19]))
	}
}

// rdpConfirm builds a TPKT + X.224 Connection Confirm with an RDP negotiation
// response of negType selecting protocol.
func rdpConfirm(negType byte, protocol uint32) []byte {
	b := make([]byte, 19)
	b[0] = 0x03 // TPKT version
	binary.BigEndian.PutUint16(b[2:4], 19)
	b[4] = 14   // X.224 LI
	b[5] = 0xD0 // CC
	b[11] = negType
	binary.LittleEndian.PutUint16(b[13:], 8)
	binary.LittleEndian.PutUint32(b[15:19], protocol)
	return b
}

func TestParseRDPConfirm(t *testing.T) {
	if s, err := parseRDPConfirm(rdpConfirm(0x02, 2)); err != nil || s != "hybrid" {
		t.Fatalf("got %q/%v, want hybrid/nil", s, err)
	}
	if s, err := parseRDPConfirm(rdpConfirm(0x02, 1)); err != nil || s != "tls" {
		t.Fatalf("got %q/%v, want tls/nil", s, err)
	}
	if s, err := parseRDPConfirm(rdpConfirm(0x03, 0)); err != nil || s != "negotiation-failure" {
		t.Fatalf("got %q/%v, want negotiation-failure/nil", s, err)
	}
	// A short Connection Confirm with no negotiation block is still RDP.
	cc := []byte{0x03, 0x00, 0x00, 0x0b, 0x06, 0xd0, 0, 0, 0, 0, 0}
	if s, err := parseRDPConfirm(cc); err != nil || s != "rdp" {
		t.Fatalf("got %q/%v, want rdp/nil", s, err)
	}
	// Not a TPKT / not a Connection Confirm must error.
	if _, err := parseRDPConfirm([]byte{0x00, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}); err == nil {
		t.Fatal("a non-TPKT response must error")
	}
	if _, err := parseRDPConfirm([]byte{0x03, 0, 0, 0x0b, 0x06, 0xe0, 0, 0, 0, 0, 0}); err == nil {
		t.Fatal("a non-CC (CR) response must error")
	}
}

func TestRDPProbeAgainstFakeServer(t *testing.T) {
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
		buf := make([]byte, 64)
		if _, err := c.Read(buf); err != nil {
			return
		}
		_, _ = c.Write(rdpConfirm(0x02, 2)) // CredSSP/NLA selected
	}()

	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	res, err := rdpProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["security"] != "hybrid" {
		t.Fatalf("security = %q, want hybrid", res.Extra["security"])
	}
}
