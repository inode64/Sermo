package conn

import (
	"context"
	"testing"
)

func TestNFSProbeAgainstFakeServer(t *testing.T) {
	port := rpcAcceptedTCPTestPort(t, 0)
	res, err := nfsProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["rpc_status"] != "success" || res.Extra["program"] != "100003" {
		t.Fatalf("extra = %v", res.Extra)
	}
}
