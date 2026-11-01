package conn

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestRsyncGreetingVersion(t *testing.T) {
	for _, greeting := range []string{
		"@RSYNCD: 31.0",
		"@RSYNCD: 31.0 sha512 sha256 sha1 md5 md4", // rsync 3.2 and later list their digests
	} {
		v, ok := rsyncGreetingVersion(greeting)
		if !ok || v != "31.0" {
			t.Fatalf("%q: got %q/%v, want 31.0/true", greeting, v, ok)
		}
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

func TestRsyncProbeKeepsTheWholeGreeting(t *testing.T) {
	const greeting = "@RSYNCD: 33.0 sha512 sha256 sha1 md5 md4"
	port := serveBanner(t, greeting+"\n", nil)
	res, err := rsyncProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "33.0" || res.Extra[extraProtocol] != "33.0" || res.Extra[extraGreeting] != greeting {
		t.Fatalf("probe = %+v", res)
	}
}

func TestRsyncProbeUnixSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "rsync.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		c, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = io.WriteString(c, "@RSYNCD: 31.0\n")
	}()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	protocol, cfg, ok := Prepare(ProtocolNameRsync, Config{Socket: socket})
	if !ok {
		t.Fatal("rsync is not registered")
	}
	res, err := protocol.Probe(ctx, cfg)
	if err != nil || res.Version != "31.0" || res.Extra[extraProtocol] != "31.0" {
		t.Fatalf("probe = %+v, %v", res, err)
	}
}
