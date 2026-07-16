package conn

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
)

func TestNUTVersion(t *testing.T) {
	if v := nutVersion("Network UPS Tools upsd 2.8.0 - http://www.networkupstools.org/"); v != "2.8.0" {
		t.Errorf("version = %q, want 2.8.0", v)
	}
	if v := nutVersion("2.7.4"); v != "2.7.4" {
		t.Errorf("bare version = %q, want 2.7.4", v)
	}
}

func TestParseNUTVarLine(t *testing.T) {
	name, val, ok := parseNUTVarLine(`VAR myups ups.status "OL CHRG"`)
	if !ok || name != "ups.status" || val != "OL CHRG" {
		t.Errorf("got (%q,%q,%v), want (ups.status, OL CHRG, true)", name, val, ok)
	}
	if _, _, ok := parseNUTVarLine("END LIST VAR myups"); ok {
		t.Error("a non-VAR line must not parse")
	}
}

func TestNUTHandshakeAnonymousNoUPS(t *testing.T) {
	// VER, then LIST UPS with zero UPSes -> stays at liveness, no LIST VAR.
	conn := rw{in: strings.NewReader("Network UPS Tools upsd 2.8.0\nBEGIN LIST UPS\nEND LIST UPS\n"), out: &bytes.Buffer{}}
	res, err := nutHandshake(conn, Config{})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if res.Version != "2.8.0" {
		t.Fatalf("version = %q, want 2.8.0", res.Version)
	}
	if _, ok := res.Extra["ups.status"]; ok {
		t.Errorf("no UPS selected, should expose no variables: %v", res.Extra)
	}
}

func TestNUTHandshakeAutoDetectSingleUPS(t *testing.T) {
	in := "Network UPS Tools upsd 2.8.0\n" +
		"BEGIN LIST UPS\nUPS apc \"APC Back-UPS\"\nEND LIST UPS\n" +
		"BEGIN LIST VAR apc\nVAR apc ups.status \"OL\"\nVAR apc battery.charge \"100\"\nEND LIST VAR apc\n"
	conn := rw{in: strings.NewReader(in), out: &bytes.Buffer{}}
	res, err := nutHandshake(conn, Config{}) // no ups configured -> auto-detect apc
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if res.Extra["ups"] != "apc" {
		t.Errorf("ups = %q, want apc", res.Extra["ups"])
	}
	if res.Extra["ups.status"] != "OL" || res.Extra["battery.charge"] != "100" {
		t.Errorf("variables not exposed: %v", res.Extra)
	}
	if res.Extra["fingerprint"] != "OL" {
		t.Errorf("fingerprint = %q, want OL (drives on_change)", res.Extra["fingerprint"])
	}
}

func TestNUTHandshakeAuthAndVars(t *testing.T) {
	in := "Network UPS Tools upsd 2.8.0\n" + // VER
		"OK\n" + // USERNAME
		"OK\n" + // PASSWORD
		"OK\n" + // LOGIN
		"BEGIN LIST VAR myups\nVAR myups ups.status \"OB DISCHRG\"\nVAR myups battery.charge \"55\"\nEND LIST VAR myups\n"
	conn := rw{in: strings.NewReader(in), out: &bytes.Buffer{}}
	res, err := nutHandshake(conn, Config{User: "mon", Password: "secret", Query: "myups"})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	sent := conn.out.String()
	for _, want := range []string{"USERNAME mon", "PASSWORD secret", "LOGIN myups", "LIST VAR myups", "LOGOUT"} {
		if !strings.Contains(sent, want) {
			t.Errorf("missing %q in sent commands:\n%s", want, sent)
		}
	}
	if strings.Contains(sent, "LIST UPS") {
		t.Errorf("explicit ups must skip auto-detect: %s", sent)
	}
	if res.Extra["ups.status"] != "OB DISCHRG" || res.Extra["battery.charge"] != "55" {
		t.Errorf("variables = %v", res.Extra)
	}
}

func TestNUTHandshakeLoginDenied(t *testing.T) {
	in := "Network UPS Tools upsd 2.8.0\n" + "OK\n" + "OK\n" + "ERR ACCESS-DENIED\n"
	assertHandshakeFails(t, nutHandshake, in, Config{User: "mon", Password: "bad", Query: "myups"})
}

func TestNUTHandshakeUnknownUPS(t *testing.T) {
	in := "Network UPS Tools upsd 2.8.0\n" + "ERR UNKNOWN-UPS\n"
	assertHandshakeFails(t, nutHandshake, in, Config{Query: "ghost"})
}

func TestNUTHandshakeVerError(t *testing.T) {
	assertHandshakeFails(t, nutHandshake, "ERR UNKNOWN-COMMAND\n", Config{})
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
		br := bufio.NewReader(c)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			switch cmd := strings.TrimSpace(line); {
			case cmd == "VER":
				_, _ = c.Write([]byte("Network UPS Tools upsd 2.8.0 - http://x/\n"))
			case cmd == "LIST UPS":
				_, _ = c.Write([]byte("BEGIN LIST UPS\nUPS apc \"APC\"\nEND LIST UPS\n"))
			case strings.HasPrefix(cmd, "LIST VAR"):
				_, _ = c.Write([]byte("BEGIN LIST VAR apc\nVAR apc ups.status \"OL\"\nVAR apc battery.charge \"100\"\nEND LIST VAR apc\n"))
			case cmd == "LOGOUT":
				_, _ = c.Write([]byte("OK Goodbye\n"))
				return
			}
		}
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
	if res.Extra["ups.status"] != "OL" || res.Extra["battery.charge"] != "100" {
		t.Fatalf("variables not exposed: %v", res.Extra)
	}
}
