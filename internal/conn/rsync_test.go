package conn

import (
	"context"
	"testing"
)

func TestRsyncGreetingVersion(t *testing.T) {
	v, ok := rsyncGreetingVersion("@RSYNCD: 31.0")
	if !ok || v != "31.0" {
		t.Fatalf("got %q/%v, want 31.0/true", v, ok)
	}
	if _, ok := rsyncGreetingVersion("HTTP/1.1 200 OK"); ok {
		t.Fatal("a non-rsync greeting must be rejected")
	}
}

func TestRsyncProbeAgainstFakeServer(t *testing.T) {
	port := serveBanner(t, "@RSYNCD: 31.0\n", nil)
	res, err := rsyncProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "31.0" {
		t.Fatalf("version = %q, want 31.0", res.Version)
	}
}
