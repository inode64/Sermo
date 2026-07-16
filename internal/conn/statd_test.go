package conn

import (
	"encoding/binary"
	"testing"
)

func TestStatdProbeAgainstFakeServer(t *testing.T) {
	assertProbeExtras(t, statdProtocol{}, rpcAcceptedTCPTestPort(t, 0),
		map[string]string{"rpc_status": "success", "program": "100024"})
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
	assertProbeExtra(t, statdProtocol{}, port, "rpc_status", "denied")
}
