package conn

import (
	"context"
	"encoding/binary"
	"net"
	"strconv"
	"testing"
)

func TestBuildRPCNull(t *testing.T) {
	b := buildRPCNull(0x12345678, portmapProg, portmapVers)
	if len(b) != 40 {
		t.Fatalf("len = %d, want 40", len(b))
	}
	for i, want := range []uint32{0x12345678, rpcCall, rpcVers, portmapProg, portmapVers, rpcProcNull} {
		if got := binary.BigEndian.Uint32(b[i*4:]); got != want {
			t.Fatalf("field %d = %d, want %d", i, got, want)
		}
	}
}

// rpcAcceptedReply builds an accepted RPC reply for xid with the given accept_stat.
func rpcAcceptedReply(xid, acceptStat uint32) []byte {
	b := make([]byte, 24)
	binary.BigEndian.PutUint32(b[0:], xid)
	binary.BigEndian.PutUint32(b[4:], rpcReply)
	binary.BigEndian.PutUint32(b[8:], rpcMsgAccepted)
	binary.BigEndian.PutUint32(b[12:], rpcAuthNone) // verf flavor
	binary.BigEndian.PutUint32(b[16:], 0)           // verf length
	binary.BigEndian.PutUint32(b[20:], acceptStat)
	return b
}

func TestParseRPCReply(t *testing.T) {
	if s, err := parseRPCReply(rpcAcceptedReply(7, 0), 7); err != nil || s != "success" {
		t.Fatalf("got %q/%v, want success/nil", s, err)
	}
	if s, err := parseRPCReply(rpcAcceptedReply(7, 2), 7); err != nil || s != "prog_mismatch" {
		t.Fatalf("got %q/%v, want prog_mismatch/nil", s, err)
	}
	if _, err := parseRPCReply(rpcAcceptedReply(7, 0), 8); err == nil {
		t.Fatal("xid mismatch must error")
	}
	// A CALL (not a REPLY) must be rejected.
	call := buildRPCNull(7, portmapProg, portmapVers)
	if _, err := parseRPCReply(call, 7); err == nil {
		t.Fatal("a non-reply message must error")
	}
	// A hostile verifier length must be rejected as truncated, never drive an
	// out-of-bounds read: the bounds check must hold even where 20+verfLen+4
	// would overflow (a near-MaxInt32 length on a 32-bit platform).
	for _, verfLen := range []uint32{0xFFFFFFFF, 0x7FFFFFF0, 24} {
		hostile := rpcAcceptedReply(7, 0)
		binary.BigEndian.PutUint32(hostile[16:], verfLen)
		if _, err := parseRPCReply(hostile, 7); err == nil {
			t.Fatalf("verifier length %#x must error", verfLen)
		}
	}
}

func TestRpcbindProbeAgainstFakeServer(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pc.Close() }()
	go func() {
		buf := make([]byte, 1500)
		n, addr, err := pc.ReadFrom(buf)
		if err != nil || n < 4 {
			return
		}
		xid := binary.BigEndian.Uint32(buf[0:4])
		_, _ = pc.WriteTo(rpcAcceptedReply(xid, 0), addr)
	}()

	_, portStr, _ := net.SplitHostPort(pc.LocalAddr().String())
	port, _ := strconv.Atoi(portStr)
	res, err := rpcbindProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["rpc_status"] != "success" {
		t.Fatalf("rpc_status = %q", res.Extra["rpc_status"])
	}
}
