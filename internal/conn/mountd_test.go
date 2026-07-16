package conn

import (
	"testing"
)

func TestMountdProbeAgainstFakeServer(t *testing.T) {
	assertProbeExtras(t, mountdProtocol{}, rpcAcceptedTCPTestPort(t, 0),
		map[string]string{"rpc_status": "success", "program": "100005"})
}

func TestMountdProbeProgMismatch(t *testing.T) {
	// prog_mismatch: still an RPC responder.
	assertProbeExtra(t, mountdProtocol{}, rpcAcceptedTCPTestPort(t, 2), "rpc_status", "prog_mismatch")
}
