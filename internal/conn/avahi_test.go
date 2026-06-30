package conn

import (
	"context"
	"testing"
	"time"
)

func TestAvahiVersion(t *testing.T) {
	if v := avahiVersion("avahi 0.8"); v != "0.8" {
		t.Fatalf("version = %q, want 0.8", v)
	}
	if v := avahiVersion("0.7"); v != "0.7" {
		t.Fatalf("version = %q, want 0.7", v)
	}
	if v := avahiVersion(""); v != "" {
		t.Fatalf("version = %q, want empty", v)
	}
}

func TestAvahiProbeUnreachableBus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// A bogus bus address must surface as an error, not hang.
	_, err := avahiProtocol{}.Probe(ctx, Config{Query: "unix:path=/nonexistent/sermo-avahi-test.sock"})
	if err == nil {
		t.Fatal("probing an unreachable bus must error")
	}
}
