package conn

import (
	"context"
	"net"
	"strings"
	"testing"
)

func TestClamdVersion(t *testing.T) {
	v, ok := clamdVersion("ClamAV 0.103.8/26900/Wed Mar 15 10:30:00 2023")
	if !ok || v != "0.103.8" {
		t.Fatalf("got %q/%v, want 0.103.8/true", v, ok)
	}
	if v, ok := clamdVersion("ClamAV 1.0.1"); !ok || v != "1.0.1" {
		t.Fatalf("got %q/%v, want 1.0.1/true", v, ok)
	}
	if _, ok := clamdVersion("HTTP/1.1 200 OK"); ok {
		t.Fatal("a non-clamd reply must be rejected")
	}
}

func TestClamdProbeAgainstFakeServer(t *testing.T) {
	port := serveOnce(t, func(c net.Conn) {
		buf := make([]byte, 64)
		n, _ := c.Read(buf)
		if !strings.Contains(string(buf[:n]), "VERSION") {
			return
		}
		_, _ = c.Write([]byte("ClamAV 0.103.8/26900/Wed Mar 15 10:30:00 2023\n"))
	})
	res, err := clamdProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "0.103.8" {
		t.Fatalf("version = %q, want 0.103.8", res.Version)
	}
	if !strings.HasPrefix(res.Extra["version_string"], "ClamAV ") {
		t.Fatalf("version_string = %q", res.Extra["version_string"])
	}
}
