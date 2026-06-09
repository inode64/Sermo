package conn

import (
	"context"
	"net"
	"strconv"
	"testing"
)

func TestSieveRegistered(t *testing.T) {
	for _, name := range []string{"sieve", "managesieve"} {
		p, ok := Lookup(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if p.DefaultPort() != 4190 {
			t.Fatalf("%s default port = %d, want 4190", name, p.DefaultPort())
		}
		if p.RequiresUser() {
			t.Fatalf("%s must not require a user", name)
		}
	}
}

func TestSieveImplementation(t *testing.T) {
	v, ok := sieveImplementation(`"IMPLEMENTATION" "Dovecot Pro v2.3"`)
	if !ok || v != "Dovecot Pro v2.3" {
		t.Fatalf("got %q/%v, want Dovecot Pro v2.3/true", v, ok)
	}
	if _, ok := sieveImplementation(`"SIEVE" "fileinto reject"`); ok {
		t.Fatal("a non-IMPLEMENTATION capability must not match")
	}
}

func serveSieve(t *testing.T, lines []string) int {
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
		for _, l := range lines {
			_, _ = c.Write([]byte(l + "\r\n"))
		}
	}()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return port
}

func TestSieveProbeGreetingOK(t *testing.T) {
	port := serveSieve(t, []string{
		`"IMPLEMENTATION" "Dovecot Pro v2.3.19"`,
		`"SIEVE" "fileinto reject envelope"`,
		`"SASL" "PLAIN LOGIN"`,
		`OK "ManageSieve ready."`,
	})
	res, err := sieveProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "Dovecot Pro v2.3.19" {
		t.Fatalf("version = %q", res.Version)
	}
	if res.Extra["implementation"] != "Dovecot Pro v2.3.19" {
		t.Fatalf("extra = %v", res.Extra)
	}
}

func TestSieveProbeRefused(t *testing.T) {
	port := serveSieve(t, []string{`BYE "Too many connections"`})
	if _, err := (sieveProtocol{}).Probe(context.Background(), Config{Host: "127.0.0.1", Port: port}); err == nil {
		t.Fatal("a BYE greeting must fail the probe")
	}
}
