package conn

import (
	"context"
	"net"
	"strconv"
	"testing"
)

func TestAsteriskGreetingVersion(t *testing.T) {
	v, ok := asteriskGreetingVersion("Asterisk Call Manager/2.10.6")
	if !ok || v != "2.10.6" {
		t.Fatalf("got %q/%v, want 2.10.6/true", v, ok)
	}
	if _, ok := asteriskGreetingVersion("220 mail ESMTP"); ok {
		t.Fatal("a non-AMI greeting must be rejected")
	}
}

func TestAsteriskProbeAgainstFakeServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = c.Write([]byte("Asterisk Call Manager/8.0.0\r\n"))
		_ = c.Close()
	}()

	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	res, err := asteriskProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "8.0.0" {
		t.Fatalf("version = %q, want 8.0.0", res.Version)
	}
	if res.Extra["banner"] != "Asterisk Call Manager/8.0.0" {
		t.Fatalf("banner = %q", res.Extra["banner"])
	}
}
