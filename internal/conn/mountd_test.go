package conn

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"testing"
)

func TestMountdProbeAgainstFakeServer(t *testing.T) {
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
		// Read the record marker + the call, echo back an accepted SUCCESS reply.
		var m [4]byte
		if _, err := io.ReadFull(c, m[:]); err != nil {
			return
		}
		n := int(binary.BigEndian.Uint32(m[:]) &^ 0x80000000)
		call := make([]byte, n)
		if _, err := io.ReadFull(c, call); err != nil {
			return
		}
		xid := binary.BigEndian.Uint32(call[0:4])
		reply := rpcAcceptedReply(xid, 0)
		hdr := make([]byte, 4)
		binary.BigEndian.PutUint32(hdr, uint32(len(reply))|0x80000000)
		_, _ = c.Write(append(hdr, reply...))
	}()

	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	res, err := mountdProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["rpc_status"] != "success" || res.Extra["program"] != "100005" {
		t.Fatalf("extra = %v", res.Extra)
	}
}

func TestMountdProbeProgMismatch(t *testing.T) {
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
		var m [4]byte
		if _, err := io.ReadFull(c, m[:]); err != nil {
			return
		}
		n := int(binary.BigEndian.Uint32(m[:]) &^ 0x80000000)
		call := make([]byte, n)
		if _, err := io.ReadFull(c, call); err != nil {
			return
		}
		xid := binary.BigEndian.Uint32(call[0:4])
		reply := rpcAcceptedReply(xid, 2) // prog_mismatch — still an RPC responder
		hdr := make([]byte, 4)
		binary.BigEndian.PutUint32(hdr, uint32(len(reply))|0x80000000)
		_, _ = c.Write(append(hdr, reply...))
	}()

	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	res, err := mountdProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["rpc_status"] != "prog_mismatch" {
		t.Fatalf("rpc_status = %q, want prog_mismatch", res.Extra["rpc_status"])
	}
}
