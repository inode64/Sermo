package conn

import (
	"testing"
)

func TestGlusterFSProbeAgainstFakeServer(t *testing.T) {
	assertProbeExtra(t, glusterfsProtocol{}, rpcAcceptedTCPTestPort(t, 0), "rpc_status", "success")
}
