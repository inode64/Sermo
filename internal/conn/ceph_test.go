package conn

import (
	"context"
	"net"
	"strconv"
	"testing"
)

func TestParseCephBanner(t *testing.T) {
	if m, ok := parseCephBanner([]byte("ceph v2\n")); !ok || m != "v2" {
		t.Fatalf("got %q/%v, want v2/true", m, ok)
	}
	if m, ok := parseCephBanner([]byte("ceph v02")); !ok || m != "v1" {
		t.Fatalf("got %q/%v, want v1/true", m, ok)
	}
	if _, ok := parseCephBanner([]byte("HTTP/1.1")); ok {
		t.Fatal("a non-Ceph banner must be rejected")
	}
}

// serveCeph writes a fixed banner and closes.
func serveCeph(t *testing.T, banner string) int {
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
		_, _ = c.Write([]byte(banner))
		_ = c.Close()
	}()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return port
}

func TestCephProbeMsgrV2(t *testing.T) {
	// "ceph v2\n" + the le16 length and feature bits that follow on the wire.
	res, err := cephProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: serveCeph(t, "ceph v2\n\x10\x00abcdefgh")})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["messenger"] != "v2" {
		t.Fatalf("messenger = %q, want v2", res.Extra["messenger"])
	}
}

func TestCephProbeMsgrV1(t *testing.T) {
	res, err := cephProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: serveCeph(t, "ceph v027\x00\x00")})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["messenger"] != "v1" {
		t.Fatalf("messenger = %q, want v1", res.Extra["messenger"])
	}
}

func TestCephProbeNotCeph(t *testing.T) {
	if _, err := (cephProtocol{}).Probe(context.Background(), Config{Host: "127.0.0.1", Port: serveCeph(t, "HTTP/1.1 200 OK\r\n")}); err == nil {
		t.Fatal("a non-Ceph banner must fail the probe")
	}
}
