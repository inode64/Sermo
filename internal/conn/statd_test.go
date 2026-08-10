package conn

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"
)

func TestStatdProbeAgainstFakeServer(t *testing.T) {
	assertProbeExtras(t, statdProtocol, rpcAcceptedTCPTestPort(t, 0),
		map[string]string{"rpc_status": "success", "program": "100024"})
}

func TestStatdProbeRejectsDeniedReply(t *testing.T) {
	port := rpcTCPTestPort(t, func(xid uint32) []byte {
		// MSG_DENIED happens before dispatching to the requested program.
		reply := make([]byte, 12)
		binary.BigEndian.PutUint32(reply[0:], xid)
		binary.BigEndian.PutUint32(reply[4:], 1) // reply
		binary.BigEndian.PutUint32(reply[8:], 1) // MSG_DENIED
		return reply
	})
	_, err := statdProtocol.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err == nil || !strings.Contains(err.Error(), "statd RPC reply: expected program 100024, got denied") {
		t.Fatalf("probe error = %v, want denied statd program", err)
	}
}
