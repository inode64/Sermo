package conn

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"testing"
)

func TestSplitSMBUser(t *testing.T) {
	cases := []struct{ in, user, domain string }{
		{`WORKGROUP\joe`, "joe", "WORKGROUP"},
		{"joe@example.com", "joe", "example.com"},
		{"joe", "joe", ""},
	}
	for _, c := range cases {
		if u, d := splitSMBUser(c.in); u != c.user || d != c.domain {
			t.Fatalf("splitSMBUser(%q) = %q/%q, want %q/%q", c.in, u, d, c.user, c.domain)
		}
	}
}

func TestSMBDialectNames(t *testing.T) {
	if smbDialectName(0x0311) != "3.1.1" || smbDialectName(0x0202) != "2.0.2" {
		t.Fatal("dialect names wrong")
	}
	if smbProtocolName(0x0311) != "SMB3" || smbProtocolName(0x0210) != "SMB2" {
		t.Fatal("protocol family wrong")
	}
}

func TestBuildSMBNegotiate(t *testing.T) {
	req, err := buildSMBNegotiate()
	if err != nil {
		t.Fatal(err)
	}
	// 4-byte direct-TCP header + 158-byte SMB2 message.
	if len(req) != 4+158 {
		t.Fatalf("len = %d, want 162", len(req))
	}
	msg := req[4:]
	if string(msg[0:4]) != "\xFESMB" {
		t.Fatalf("protocol id = % x", msg[0:4])
	}
	if binary.LittleEndian.Uint16(msg[12:]) != 0 { // Command NEGOTIATE
		t.Fatal("command must be NEGOTIATE (0)")
	}
	if binary.LittleEndian.Uint16(msg[64+2:]) != 5 { // DialectCount
		t.Fatalf("dialect count = %d, want 5", binary.LittleEndian.Uint16(msg[64+2:]))
	}
	if binary.LittleEndian.Uint32(msg[64+28:]) != 112 { // NegotiateContextOffset
		t.Fatal("negotiate context offset must be 112")
	}
}

// fakeNegotiateResp builds an SMB2 NEGOTIATE response carrying dialect and
// securityMode (without the direct-TCP frame).
func fakeNegotiateResp(dialect, securityMode uint16) []byte {
	resp := make([]byte, 72)
	copy(resp[0:4], []byte{0xFE, 'S', 'M', 'B'})
	binary.LittleEndian.PutUint16(resp[4:], 64)
	binary.LittleEndian.PutUint16(resp[64:], 65) // body StructureSize
	binary.LittleEndian.PutUint16(resp[66:], securityMode)
	binary.LittleEndian.PutUint16(resp[68:], dialect)
	return resp
}

func TestParseSMBNegotiate(t *testing.T) {
	d, signing, err := parseSMBNegotiate(fakeNegotiateResp(0x0311, 0x0003))
	if err != nil || d != 0x0311 || !signing {
		t.Fatalf("got %#x/%v/%v, want 0x0311/true/nil", d, signing, err)
	}
	if _, signing, _ := parseSMBNegotiate(fakeNegotiateResp(0x0210, 0x0001)); signing {
		t.Fatal("signing must be false when the required bit is clear")
	}
	if _, _, err := parseSMBNegotiate([]byte("HTTP/1.1 200 OK............................................................")); err == nil {
		t.Fatal("a non-SMB2 response must error")
	}
}

func TestSMBProbeNegotiateOnly(t *testing.T) {
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
		// Drain the framed NEGOTIATE request.
		var h [4]byte
		if _, err := io.ReadFull(c, h[:]); err != nil {
			return
		}
		n := int(h[1])<<16 | int(h[2])<<8 | int(h[3])
		if _, err := io.ReadFull(c, make([]byte, n)); err != nil {
			return
		}
		resp := fakeNegotiateResp(0x0311, 0x0003) // SMB 3.1.1, signing required
		_, _ = c.Write(append([]byte{0x00, byte(len(resp) >> 16), byte(len(resp) >> 8), byte(len(resp))}, resp...))
	}()

	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	// No user -> negotiate-only path (no library session).
	res, err := smbProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "3.1.1" {
		t.Fatalf("version = %q, want 3.1.1", res.Version)
	}
	if res.Extra["protocol"] != "SMB3" || res.Extra["signing_required"] != "true" {
		t.Fatalf("extra = %v", res.Extra)
	}
}
