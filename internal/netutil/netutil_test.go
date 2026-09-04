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

func TestTLSClientConfigForMode(t *testing.T) {
	tests := []struct {
		name       string
		mode       string
		skipVerify bool
	}{
		{name: "empty is verified", mode: ""},
		{name: "true is verified", mode: TLSModeTrue},
		{name: "yes is verified", mode: "yes"},
		{name: "skip-verify", mode: TLSModeSkipVerify, skipVerify: true},
		{name: "skip-verify spelling", mode: "  SKIP-VERIFY  ", skipVerify: true},
		{name: "custom stays verified", mode: "verify-full"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := TLSClientConfigForMode("service.example", test.mode)
			if cfg.ServerName != "service.example" || cfg.MinVersion != tls.VersionTLS12 {
				t.Fatalf("TLSClientConfigForMode() = server %q min %x", cfg.ServerName, cfg.MinVersion)
			}
			if cfg.InsecureSkipVerify != test.skipVerify {
				t.Fatalf("InsecureSkipVerify = %t, want %t", cfg.InsecureSkipVerify, test.skipVerify)
			}
		})
	}
	first := TLSClientConfigForMode("service.example", TLSModeSkipVerify)
	second := TLSClientConfigForMode("service.example", TLSModeSkipVerify)
	if first == second {
		t.Fatal("TLSClientConfigForMode reused mutable configuration")
	}
}

func TestNormalizeTLS(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: ""},
		{name: "disabled", in: "off"},
		{name: "verified", in: "yes", want: TLSModeTrue},
		{name: "skip verify", in: "  SKIP-VERIFY  ", want: TLSModeSkipVerify},
		{name: "typo stays custom", in: "skipverify", want: "skipverify"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := NormalizeTLS(test.in); got != test.want {
				t.Fatalf("NormalizeTLS(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}
