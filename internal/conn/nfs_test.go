package conn

import (
	"context"
	"strings"
	"testing"
)

func TestNFSProbeAgainstFakeServer(t *testing.T) {
	assertProbeExtras(t, nfsProtocol{}, rpcAcceptedTCPTestPort(t, 0),
		map[string]string{"rpc_status": "success", "program": "100003"})
}

func TestRPCNullProbesRejectUnavailableProgram(t *testing.T) {
	tests := []struct {
		name      string
		protocol  Protocol
		wantError string
	}{
		{name: "nfs", protocol: nfsProtocol{}, wantError: "nfs RPC reply: expected program 100003, got prog_unavail"},
		{name: "mountd", protocol: mountdProtocol, wantError: "mountd RPC reply: expected program 100005, got prog_unavail"},
		{name: "statd", protocol: statdProtocol, wantError: "statd RPC reply: expected program 100024, got prog_unavail"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.protocol.Probe(context.Background(), Config{
				Host: "127.0.0.1",
				Port: rpcAcceptedTCPTestPort(t, rpcAcceptProgUnavail),
			})
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("probe error = %v, want containing %q", err, tt.wantError)
			}
		})
	}
}
