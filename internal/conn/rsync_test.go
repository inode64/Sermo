package conn

import (
	"context"
	"net"
	"strconv"
	"testing"
)

func TestRsyncRegistered(t *testing.T) {
	for _, name := range []string{"rsync", "rsyncd"} {
		p, ok := Lookup(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if p.DefaultPort() != 873 {
			t.Fatalf("%s default port = %d, want 873", name, p.DefaultPort())
		}
		if p.RequiresUser() {
			t.Fatalf("%s must not require a user", name)
		}
	}
}

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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = c.Write([]byte("@RSYNCD: 31.0\n"))
		_ = c.Close()
	}()

	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	res, err := rsyncProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "31.0" {
		t.Fatalf("version = %q, want 31.0", res.Version)
	}
}
