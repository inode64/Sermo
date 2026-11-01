package conn

import (
	"bytes"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"sermo/internal/units"
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

// floodReadWriter repeats chunk for reads, up to flood bytes, then reports EOF.
// It records how much a reader consumed, so a bound shows as bytes, not time.
type floodReadWriter struct {
	chunk []byte
	flood int
	read  int
}

func (f *floodReadWriter) Read(p []byte) (int, error) {
	if f.read >= f.flood {
		return 0, io.EOF
	}
	n := copy(p, f.chunk[f.read%len(f.chunk):])
	f.read += n
	return n, nil
}

func (f *floodReadWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestReadTextGreetingBoundsTheReply(t *testing.T) {
	const (
		flood     = 8 * units.BytesPerMiB
		readAhead = 4 * units.BytesPerKiB // bufio's default buffer
	)
	tests := []struct {
		name    string
		chunk   string
		maxRead int
		wantErr string
	}{
		{
			name:    "line without an end",
			chunk:   "a",
			maxRead: maxProtocolLineBytes + readAhead,
			wantErr: "protocol line exceeds",
		},
		{
			name:    "reply that never ends",
			chunk:   "220-" + strings.Repeat("a", 1024) + "\r\n",
			maxRead: maxTextExchangeBytes + readAhead,
			wantErr: "text exchange exceeds",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			peer := &floodReadWriter{chunk: []byte(test.chunk), flood: flood}
			_, _, _, err := readTextGreeting(peer)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("readTextGreeting() error = %v, want %q", err, test.wantErr)
			}
			if peer.read > test.maxRead {
				t.Fatalf("read %d bytes of the peer's reply, want at most %d", peer.read, test.maxRead)
			}
		})
	}
}

func TestReadTextGreetingLineBoundCountsItsTerminator(t *testing.T) {
	const prefix, crlf = "220 ", "\r\n"
	fits := prefix + strings.Repeat("a", maxProtocolLineBytes-len(prefix)-len(crlf)) + crlf
	if _, code, _, err := readTextGreeting(rw{in: strings.NewReader(fits), out: &bytes.Buffer{}}); err != nil || code != 220 {
		t.Fatalf("a line of exactly the limit: code %d, error %v", code, err)
	}
	over := prefix + strings.Repeat("a", maxProtocolLineBytes-len(prefix)-len(crlf)+1) + crlf
	if _, _, _, err := readTextGreeting(rw{in: strings.NewReader(over), out: &bytes.Buffer{}}); err == nil {
		t.Fatal("a line one byte over the limit was accepted")
	}
}

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
