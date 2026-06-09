package conn

import (
	"context"
	"testing"
	"time"
)

func TestAvahiRegistered(t *testing.T) {
	for _, name := range []string{"avahi", "avahi-daemon"} {
		p, ok := Lookup(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if p.DefaultPort() != 0 {
			t.Fatalf("%s default port = %d, want 0 (socket-based)", name, p.DefaultPort())
		}
		if p.RequiresUser() {
			t.Fatalf("%s must not require a user", name)
		}
	}
}

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
