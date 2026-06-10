package conn

import (
	"bytes"
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
)

func TestNUTRegistered(t *testing.T) {
	for _, name := range []string{"nut", "ups", "upsd"} {
		p, ok := Lookup(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if p.DefaultPort() != 3493 {
			t.Fatalf("%s default port = %d, want 3493", name, p.DefaultPort())
		}
		if p.RequiresUser() {
			t.Fatalf("%s must not require a user (anonymous VER probe allowed)", name)
		}
	}
}

func TestNUTVersion(t *testing.T) {
	if v := nutVersion("Network UPS Tools upsd 2.8.0 - http://www.networkupstools.org/"); v != "2.8.0" {
		t.Errorf("version = %q, want 2.8.0", v)
	}
	if v := nutVersion("2.7.4"); v != "2.7.4" {
		t.Errorf("bare version = %q, want 2.7.4", v)
	}
}

func TestParseNUTVar(t *testing.T) {
	if v, ok := parseNUTVar(`VAR myups ups.status "OL CHRG"`); !ok || v != "OL CHRG" {
		t.Errorf("got %q/%v, want \"OL CHRG\"/true", v, ok)
	}
	if _, ok := parseNUTVar("ERR UNKNOWN-UPS"); ok {
		t.Error("a non-VAR line must not parse")
	}
}

func TestNUTHandshakeAnonymous(t *testing.T) {
	conn := rw{in: strings.NewReader("Network UPS Tools upsd 2.8.0 - http://x/\n"), out: &bytes.Buffer{}}
	res, err := nutHandshake(conn, Config{})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if res.Version != "2.8.0" {
		t.Fatalf("version = %q, want 2.8.0", res.Version)
	}
	if strings.Contains(conn.out.String(), "USERNAME") {
		t.Fatalf("anonymous check must not authenticate: %q", conn.out.String())
	}
}

func TestNUTHandshakeAuthAndStatus(t *testing.T) {
	replies := "Network UPS Tools upsd 2.8.0\n" + // VER
		"OK\n" + // USERNAME
		"OK\n" + // PASSWORD
		"OK\n" + // LOGIN
		"VAR myups ups.status \"OL\"\n" // GET VAR
	conn := rw{in: strings.NewReader(replies), out: &bytes.Buffer{}}
	res, err := nutHandshake(conn, Config{User: "mon", Password: "secret", Query: "myups"})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	sent := conn.out.String()
	for _, want := range []string{"USERNAME mon", "PASSWORD secret", "LOGIN myups", "GET VAR myups ups.status", "LOGOUT"} {
		if !strings.Contains(sent, want) {
			t.Errorf("missing %q in sent commands:\n%s", want, sent)
		}
	}
	if res.Extra["ups.status"] != "OL" {
		t.Errorf("ups.status = %q, want OL", res.Extra["ups.status"])
	}
}

func TestNUTHandshakeLoginDenied(t *testing.T) {
	replies := "Network UPS Tools upsd 2.8.0\n" + "OK\n" + "OK\n" + "ERR ACCESS-DENIED\n"
	conn := rw{in: strings.NewReader(replies), out: &bytes.Buffer{}}
	if _, err := nutHandshake(conn, Config{User: "mon", Password: "bad", Query: "myups"}); err == nil {
		t.Fatal("a denied LOGIN must fail")
	}
}

func TestNUTHandshakeUnknownUPS(t *testing.T) {
	replies := "Network UPS Tools upsd 2.8.0\n" + "ERR UNKNOWN-UPS\n"
	conn := rw{in: strings.NewReader(replies), out: &bytes.Buffer{}}
	if _, err := nutHandshake(conn, Config{Query: "ghost"}); err == nil {
		t.Fatal("an UNKNOWN-UPS status read must fail")
	}
}

func TestNUTHandshakeVerError(t *testing.T) {
	conn := rw{in: strings.NewReader("ERR UNKNOWN-COMMAND\n"), out: &bytes.Buffer{}}
	if _, err := nutHandshake(conn, Config{}); err == nil {
		t.Fatal("an ERR reply to VER must fail")
	}
}

func TestNUTProbeAgainstFakeServer(t *testing.T) {
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
		defer func() { _ = c.Close() }()
		buf := make([]byte, 64)
		n, _ := c.Read(buf)
		if !strings.HasPrefix(string(buf[:n]), "VER") {
			return
		}
		_, _ = c.Write([]byte("Network UPS Tools upsd 2.8.0 - http://x/\n"))
	}()

	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	res, err := nutProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "2.8.0" {
		t.Fatalf("version = %q, want 2.8.0", res.Version)
	}
}
