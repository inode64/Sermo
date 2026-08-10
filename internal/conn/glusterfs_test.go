package conn

import (
	"context"
	"net"
	"testing"
)

func TestGlusterFSProbeAgainstFakeServer(t *testing.T) {
	port := serveOnce(t, func(_ net.Conn) {})
	if _, err := (glusterfsProtocol{}).Probe(context.Background(), Config{Host: "127.0.0.1", Port: port}); err != nil {
		t.Fatalf("probe: %v", err)
	}
}
