package conn

import (
	"context"
	"testing"
)

func TestGlusterFSProbeAgainstFakeServer(t *testing.T) {
	port := rpcAcceptedTCPTestPort(t, 0)
	res, err := glusterfsProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["rpc_status"] != "success" {
		t.Fatalf("rpc_status = %q", res.Extra["rpc_status"])
	}
}
