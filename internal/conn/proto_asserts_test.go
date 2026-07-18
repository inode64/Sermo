package conn

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// assertProbeExtra probes proto against a local server on port and asserts the
// Extra value recorded under key.
func assertProbeExtra(t *testing.T, proto Protocol, port int, key, want string) {
	t.Helper()
	res, err := proto.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra[key] != want {
		t.Fatalf("%s = %q, want %q", key, res.Extra[key], want)
	}
}

// assertProbeExtras probes proto against a local server on port and asserts every
// key/value pair in want is present in the recorded Extra map.
func assertProbeExtras(t *testing.T, proto Protocol, port int, want map[string]string) {
	t.Helper()
	res, err := proto.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	for key, val := range want {
		if res.Extra[key] != val {
			t.Fatalf("%s = %q, want %q", key, res.Extra[key], val)
		}
	}
}

// assertProbeVersion probes proto against a local server on port and asserts the
// reported Version plus one Extra key/value.
func assertProbeVersion(t *testing.T, proto Protocol, port int, wantVersion, extraKey, wantExtra string) {
	t.Helper()
	res, err := proto.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != wantVersion {
		t.Fatalf("version = %q, want %q", res.Version, wantVersion)
	}
	if res.Extra[extraKey] != wantExtra {
		t.Fatalf("%s = %q, want %q", extraKey, res.Extra[extraKey], wantExtra)
	}
}

// runMapCases exercises a pure mapping function over an input→want table.
func runMapCases[K, V comparable](t *testing.T, fnName string, fn func(K) V, cases map[K]V) {
	t.Helper()
	for in, want := range cases {
		if got := fn(in); got != want {
			t.Errorf("%s(%v) = %v, want %v", fnName, in, got, want)
		}
	}
}

// serverHostPort parses a test server's URL (http or https) into host and port.
func serverHostPort(t *testing.T, srv *httptest.Server) (string, int) {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	return u.Hostname(), port
}

// serveJSON starts a plain-HTTP test server answering path with body (404
// elsewhere) and returns its host and port.
func serveJSON(t *testing.T, path, body string) (string, int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return serverHostPort(t, srv)
}

// deadPort returns a loopback port guaranteed to be closed: it binds :0, notes
// the port and immediately releases it so the address refuses connections.
func deadPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()
	port, _ := strconv.Atoi(portStr)
	return port
}

// assertProbeRefused asserts that probing proto on a closed port returns an error
// within a bounded time rather than hanging.
func assertProbeRefused(t *testing.T, proto Protocol, port int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := proto.Probe(ctx, Config{Host: "127.0.0.1", Port: port}); err == nil {
		t.Fatal("probing a closed port must error")
	}
}

// handshakeFn is the shared signature of the line-greeting protocol handshakes
// (ftp/pop/imap/smtp/nut/redis).
type handshakeFn func(rwc io.ReadWriter, cfg Config) (Result, error)

// newRW pairs canned server replies (read side) with a fresh capture buffer
// (write side).
func newRW(replies string) *rw {
	return &rw{in: strings.NewReader(replies), out: &bytes.Buffer{}}
}

// assertHandshakeAnonymous runs an unauthenticated handshake and asserts it
// succeeds without sending mustNotSend and captures wantGreetingSubstr in the
// greeting Extra.
func assertHandshakeAnonymous(t *testing.T, fn handshakeFn, replies, mustNotSend, wantGreetingSubstr string) {
	t.Helper()
	conn := newRW(replies)
	res, err := fn(conn, Config{})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if strings.Contains(conn.out.String(), mustNotSend) {
		t.Fatalf("anonymous check must not send %s: %q", mustNotSend, conn.out.String())
	}
	if !strings.Contains(res.Extra["greeting"], wantGreetingSubstr) {
		t.Fatalf("greeting not captured: %v", res.Extra)
	}
}

// assertHandshakeLogin runs an authenticated handshake and asserts it succeeds
// after sending every mustSend fragment (e.g. "USER joe", "PASS secret").
func assertHandshakeLogin(t *testing.T, fn handshakeFn, replies string, cfg Config, mustSend ...string) {
	t.Helper()
	conn := newRW(replies)
	if _, err := fn(conn, cfg); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	for _, fragment := range mustSend {
		if !strings.Contains(conn.out.String(), fragment) {
			t.Fatalf("%s not sent: %q", fragment, conn.out.String())
		}
	}
}

// assertHandshakeFails runs a handshake expected to return an error.
func assertHandshakeFails(t *testing.T, fn handshakeFn, replies string, cfg Config) {
	t.Helper()
	if _, err := fn(newRW(replies), cfg); err == nil {
		t.Fatal("handshake must fail")
	}
}

// serveOnce listens on a loopback port, accepts a single connection and hands it
// to onConn, then returns the port. The listener and connection are closed on
// cleanup / when onConn returns.
func serveOnce(t *testing.T, onConn func(c net.Conn)) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		onConn(c)
	}()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return port
}

// serveUDPOnce listens on a loopback UDP port, reads one datagram and hands it to
// reply; a non-nil return is written back to the sender. Returns the port.
func serveUDPOnce(t *testing.T, reply func(req []byte) []byte) int {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		if out := reply(buf[:n]); out != nil {
			_, _ = pc.WriteTo(out, addr)
		}
	}()
	_, portStr, _ := net.SplitHostPort(pc.LocalAddr().String())
	port, _ := strconv.Atoi(portStr)
	return port
}

// serveBanner serves a single connection that optionally reads one line into
// gotLine, then writes reply. A nil gotLine skips the read.
func serveBanner(t *testing.T, reply string, gotLine *string) int {
	t.Helper()
	return serveOnce(t, func(c net.Conn) {
		if gotLine != nil {
			line, _ := bufio.NewReader(c).ReadString('\n')
			*gotLine = line
		}
		_, _ = c.Write([]byte(reply))
	})
}
