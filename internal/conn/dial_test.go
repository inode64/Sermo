package conn

import (
	"errors"
	"io"
	"strings"
	"testing"
)

type failingReadWriter struct {
	err error
}

func (f failingReadWriter) Read([]byte) (int, error)    { return 0, f.err }
func (f failingReadWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestReadTextGreetingWrapsReadError(t *testing.T) {
	wantErr := errors.New("greeting unavailable")
	_, _, _, err := readTextGreeting(failingReadWriter{err: wantErr})
	if !errors.Is(err, wantErr) {
		t.Fatalf("readTextGreeting() error = %v, want wrapped %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), "read text greeting") {
		t.Fatalf("readTextGreeting() error = %q, want greeting context", err)
	}
}

var _ io.ReadWriter = failingReadWriter{}

// resolveUDPAddr replaces net.ResolveUDPAddr in the UDP probes so a name lookup
// is bounded by the probe's context; literal addresses resolve as before.
func TestResolveUDPAddrLiterals(t *testing.T) {
	for _, tc := range []struct {
		network, address, want string
	}{
		{networkIP, "192.0.2.10:69", "192.0.2.10:69"},
		{networkIP4, "192.0.2.10:67", "192.0.2.10:67"},
		{networkIP, "[2001:db8::1]:69", "[2001:db8::1]:69"},
	} {
		got, err := resolveUDPAddr(t.Context(), tc.network, tc.address)
		if err != nil || got.String() != tc.want {
			t.Fatalf("resolveUDPAddr(%s, %s) = %v, %v; want %s", tc.network, tc.address, got, err, tc.want)
		}
	}
	if _, err := resolveUDPAddr(t.Context(), networkIP4, "[2001:db8::1]:67"); err == nil {
		t.Fatal("an IPv6 literal resolved for ip4")
	}
	if _, err := resolveUDPAddr(t.Context(), networkIP, "192.0.2.10"); err == nil {
		t.Fatal("an address without a port resolved")
	}
}
