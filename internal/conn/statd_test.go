package conn

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"testing"
)

func TestStatdRegistered(t *testing.T) {
	for _, name := range []string{"statd", "rpc.statd", "nsm", "nfs-statd"} {
		p, ok := Lookup(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if p.DefaultPort() != 662 {
			t.Fatalf("%s default port = %d, want 662", name, p.DefaultPort())
		}
		if p.RequiresUser() {
			t.Fatalf("%s must not require a user", name)
		}
	}
}

func TestStatdProbeAgainstFakeServer(t *testing.T) {
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
	res, err := statdProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["rpc_status"] != "success" || res.Extra["program"] != "100024" {
		t.Fatalf("extra = %v", res.Extra)
	}
}

func TestStatdProbeDenied(t *testing.T) {
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
		// MSG_DENIED reply (reply_stat = 1) — still proves an RPC responder.
		reply := make([]byte, 12)
		binary.BigEndian.PutUint32(reply[0:], xid)
		binary.BigEndian.PutUint32(reply[4:], 1) // reply
		binary.BigEndian.PutUint32(reply[8:], 1) // MSG_DENIED
		hdr := make([]byte, 4)
		binary.BigEndian.PutUint32(hdr, uint32(len(reply))|0x80000000)
		_, _ = c.Write(append(hdr, reply...))
	}()

	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	res, err := statdProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["rpc_status"] != "denied" {
		t.Fatalf("rpc_status = %q, want denied", res.Extra["rpc_status"])
	}
}
