package conn

import (
	"errors"
	"io"
	"net"
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
// is bounded by the probe's context; literal addresses resolve as before,
// including a link-local literal's zone.
func TestResolveUDPAddrLiterals(t *testing.T) {
	for _, tc := range []struct {
		network, address, want string
	}{
		{networkUDP, "192.0.2.10:69", "192.0.2.10:69"},
		{networkUDP4, "192.0.2.10:67", "192.0.2.10:67"},
		{networkUDP, "[2001:db8::1]:69", "[2001:db8::1]:69"},
		{networkUDP, "[fe80::1%lo]:69", "[fe80::1%lo]:69"},
	} {
		got, err := resolveUDPAddr(t.Context(), tc.network, tc.address)
		if err != nil || got.String() != tc.want {
			t.Fatalf("resolveUDPAddr(%s, %s) = %v, %v; want %s", tc.network, tc.address, got, err, tc.want)
		}
	}
	if _, err := resolveUDPAddr(t.Context(), networkUDP4, "[2001:db8::1]:67"); err == nil {
		t.Fatal("an IPv6 literal resolved for udp4")
	}
	if _, err := resolveUDPAddr(t.Context(), networkUDP, "192.0.2.10"); err == nil {
		t.Fatal("an address without a port resolved")
	}
}

// A name with both families resolves as net.ResolveUDPAddr would: IPv4 first,
// whatever order the resolver answered in.
func TestPickUDPAddrPrefersIPv4(t *testing.T) {
	v6, v4 := net.IPAddr{IP: net.ParseIP("2001:db8::1")}, net.IPAddr{IP: net.ParseIP("192.0.2.10")}
	for _, tc := range []struct {
		network, host string
		ips           []net.IPAddr
		want          string
		ok            bool
	}{
		{networkUDP, "tftp.example", []net.IPAddr{v6, v4}, "192.0.2.10", true},
		{networkUDP, "tftp.example", []net.IPAddr{v6}, "2001:db8::1", true},
		{networkUDP, "2001:db8::1", []net.IPAddr{v6}, "2001:db8::1", true},
		{networkUDP4, "dhcp.example", []net.IPAddr{v6, v4}, "192.0.2.10", true},
		{networkUDP4, "dhcp.example", []net.IPAddr{v6}, "", false},
		{networkUDP, "empty.example", nil, "", false},
	} {
		got, ok := pickUDPAddr(tc.network, tc.host, tc.ips)
		if ok != tc.ok || (ok && got.IP.String() != tc.want) {
			t.Fatalf("pickUDPAddr(%s, %s, %v) = %v, %v; want %s, %v", tc.network, tc.host, tc.ips, got, ok, tc.want, tc.ok)
		}
	}
}
