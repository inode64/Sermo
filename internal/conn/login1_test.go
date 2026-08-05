package conn

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLogin1ProbeUsesPreResolvedDBusAddress(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	const address = "unix:path=/nonexistent/sermo-login1-test.sock"
	_, err := login1Protocol{}.Probe(ctx, Config{Socket: address})
	if err == nil {
		t.Fatal("Probe() error = nil, want unreachable D-Bus error")
	}
	if got := err.Error(); strings.Contains(got, "unix:path=unix:path=") {
		t.Fatalf("Probe() double-wrapped the pre-resolved address: %v", err)
	}
	if got := err.Error(); !strings.Contains(got, address) {
		t.Fatalf("Probe() error = %v, want address %q", err, address)
	}
}
