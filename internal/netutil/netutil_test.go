package netutil

import (
	"context"
	"crypto/tls"
	"testing"
	"time"
)

// TimeoutFromContext returns the context's remaining time, the fallback with no
// deadline, or 1ns when already past. Pin all three branches.
func TestTimeoutFromContext(t *testing.T) {
	if got := TimeoutFromContext(context.Background(), 10*time.Second); got != 10*time.Second {
		t.Errorf("no deadline = %v, want the 10s fallback", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	if got := TimeoutFromContext(ctx, 10*time.Second); got <= 0 || got > time.Hour {
		t.Errorf("future deadline = %v, want within (0, 1h]", got)
	}

	past, cancel2 := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel2()
	if got := TimeoutFromContext(past, 10*time.Second); got != time.Nanosecond {
		t.Errorf("past deadline = %v, want 1ns fail-fast", got)
	}
}

func TestTLSClientConfig(t *testing.T) {
	first := TLSClientConfig("service.example")
	second := TLSClientConfig("service.example")
	if first == second {
		t.Fatal("TLSClientConfig reused mutable configuration")
	}
	if first.ServerName != "service.example" || first.MinVersion != tls.VersionTLS12 {
		t.Fatalf("TLSClientConfig() = server %q min %x, want service.example and TLS 1.2", first.ServerName, first.MinVersion)
	}
	if first.InsecureSkipVerify {
		t.Fatal("TLSClientConfig disabled certificate verification by default")
	}
	first.InsecureSkipVerify = true
	if second.InsecureSkipVerify {
		t.Fatal("mutating one TLSClientConfig result changed another")
	}
}
