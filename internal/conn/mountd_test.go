package conn

import (
	"context"
	"testing"
)

func TestMountdProbeAgainstFakeServer(t *testing.T) {
	port := rpcAcceptedTCPTestPort(t, 0)
	res, err := mountdProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["rpc_status"] != "success" || res.Extra["program"] != "100005" {
		t.Fatalf("extra = %v", res.Extra)
	}
}

func TestMountdProbeProgMismatch(t *testing.T) {
	port := rpcAcceptedTCPTestPort(t, 2) // prog_mismatch: still an RPC responder.
	res, err := mountdProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["rpc_status"] != "prog_mismatch" {
		t.Fatalf("rpc_status = %q, want prog_mismatch", res.Extra["rpc_status"])
	}
}
