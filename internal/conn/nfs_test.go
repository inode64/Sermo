package conn

import (
	"testing"
)

func TestNFSProbeAgainstFakeServer(t *testing.T) {
	assertProbeExtras(t, nfsProtocol{}, rpcAcceptedTCPTestPort(t, 0),
		map[string]string{"rpc_status": "success", "program": "100003"})
}
