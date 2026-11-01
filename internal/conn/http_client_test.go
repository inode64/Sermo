package conn

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHTTPProbeBaseEscapesAnIPv6Zone(t *testing.T) {
	const zoned = "fe80::1%eth0"
	_, base := httpProbeBase(Config{Host: zoned, Port: 8080}, 8080)
	parsed, err := url.Parse(base + "/metrics")
	if err != nil {
		t.Fatalf("parse %q: %v", base, err)
	}
	if parsed.Hostname() != zoned || parsed.Port() != "8080" {
		t.Fatalf("host = %q port = %q, want %q 8080", parsed.Hostname(), parsed.Port(), zoned)
	}
}

// Every HTTP-based protocol must get as far as dialing a zoned address: the
// URL it builds is the one thing they share.
func TestHTTPProbesReachTheDialOnAZonedHost(t *testing.T) {
	for _, name := range []string{
		ProtocolNameCloudflared, ProtocolNameInfluxDB, ProtocolNameIPP, ProtocolNamePrometheus,
		ProtocolNameRspamd, ProtocolNameSyncthing, ProtocolNameUniFi,
	} {
		t.Run(name, func(t *testing.T) {
			proto, ok := Lookup(name)
			if !ok {
				t.Fatalf("%s is not registered", name)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			_, err := proto.Probe(ctx, Config{Host: "fe80::1%lo", Port: 1})
			if err != nil && strings.Contains(err.Error(), "invalid URL escape") {
				t.Fatalf("probe never dialed: %v", err)
			}
		})
	}
}

func TestHTTPProbeAnswersOnAZonedLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(respPong))
	}))
	_ = server.Listener.Close()
	server.Listener = ln
	server.Start()
	t.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := (rspamdProtocol{}).Probe(ctx, Config{Host: "::1%lo", Port: portNumber}); err != nil {
		t.Fatalf("probe: %v", err)
	}
}
