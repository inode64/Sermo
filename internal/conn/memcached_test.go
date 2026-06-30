package conn

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
)

// fakeMemcached serves one connection that replies to `stats` with reply, then
// closes. It returns the listening port.
func fakeMemcached(t *testing.T, reply string) int {
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
		buf := make([]byte, 64)
		n, _ := c.Read(buf)
		if !strings.HasPrefix(string(buf[:n]), "stats") {
			return
		}
		_, _ = c.Write([]byte(reply))
	}()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return port
}

func TestMemcachedProbeAgainstFakeServer(t *testing.T) {
	const stats = "STAT pid 1234\r\n" +
		"STAT uptime 3600\r\n" +
		"STAT version 1.6.21\r\n" +
		"STAT curr_connections 10\r\n" +
		"STAT cmd_get 100\r\n" +
		"STAT get_hits 80\r\n" +
		"STAT get_misses 20\r\n" +
		"STAT curr_items 50\r\n" +
		"STAT bytes 1024\r\n" +
		"STAT limit_maxbytes 67108864\r\n" +
		"STAT evictions 0\r\n" +
		"END\r\n"
	port := fakeMemcached(t, stats)

	res, err := memcachedProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "1.6.21" {
		t.Fatalf("version = %q, want 1.6.21", res.Version)
	}
	for k, want := range map[string]string{
		"uptime":           "3600",
		"curr_connections": "10",
		"get_hits":         "80",
		"get_misses":       "20",
		"limit_maxbytes":   "67108864",
		"evictions":        "0",
	} {
		if got := res.Extra[k]; got != want {
			t.Errorf("Extra[%q] = %q, want %q", k, got, want)
		}
	}
	// `pid` is not in the curated set, so it must not be published.
	if _, ok := res.Extra["pid"]; ok {
		t.Errorf("pid should not be published, got %q", res.Extra["pid"])
	}
}

func TestMemcachedProbeRejectsNonMemcached(t *testing.T) {
	// A server that does not speak the stats protocol (e.g. an HTTP endpoint or
	// an error reply) must fail the probe rather than report healthy.
	port := fakeMemcached(t, "ERROR\r\n")
	if _, err := (memcachedProtocol{}).Probe(context.Background(), Config{Host: "127.0.0.1", Port: port}); err == nil {
		t.Fatal("a non-STAT reply must fail the probe")
	}
}
