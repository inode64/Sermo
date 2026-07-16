package conn

import (
	"context"
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
	return serveBanner(t, banner, nil)
}

func TestCephProbeMsgrV2(t *testing.T) {
	// "ceph v2\n" + the le16 length and feature bits that follow on the wire.
	assertProbeExtra(t, cephProtocol{}, serveCeph(t, "ceph v2\n\x10\x00abcdefgh"), "messenger", "v2")
}

func TestCephProbeMsgrV1(t *testing.T) {
	assertProbeExtra(t, cephProtocol{}, serveCeph(t, "ceph v027\x00\x00"), "messenger", "v1")
}

func TestCephProbeNotCeph(t *testing.T) {
	if _, err := (cephProtocol{}).Probe(context.Background(), Config{Host: "127.0.0.1", Port: serveCeph(t, "HTTP/1.1 200 OK\r\n")}); err == nil {
		t.Fatal("a non-Ceph banner must fail the probe")
	}
}
