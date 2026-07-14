package conn

import (
	"context"
	"encoding/binary"
	"testing"
)

func TestStatdProbeAgainstFakeServer(t *testing.T) {
	port := rpcAcceptedTCPTestPort(t, 0)
	res, err := statdProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["rpc_status"] != "success" || res.Extra["program"] != "100024" {
		t.Fatalf("extra = %v", res.Extra)
	}
}

func TestStatdProbeDenied(t *testing.T) {
	port := rpcTCPTestPort(t, func(xid uint32) []byte {
		// MSG_DENIED reply (reply_stat = 1) — still proves an RPC responder.
		reply := make([]byte, 12)
		binary.BigEndian.PutUint32(reply[0:], xid)
		binary.BigEndian.PutUint32(reply[4:], 1) // reply
		binary.BigEndian.PutUint32(reply[8:], 1) // MSG_DENIED
		return reply
	})
	res, err := statdProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["rpc_status"] != "denied" {
		t.Fatalf("rpc_status = %q, want denied", res.Extra["rpc_status"])
	}
}
