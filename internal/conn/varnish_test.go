package conn

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"testing"
)

func TestVarnishRegistered(t *testing.T) {
	for _, name := range []string{"varnish", "varnishadm"} {
		p, ok := Lookup(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if p.DefaultPort() != 6082 {
			t.Fatalf("%s default port = %d, want 6082", name, p.DefaultPort())
		}
		if p.RequiresUser() {
			t.Fatalf("%s must not require a user", name)
		}
	}
}

func TestParseVarnishStatus(t *testing.T) {
	if s, l, err := parseVarnishStatus("200 8       \n"); err != nil || s != 200 || l != 8 {
		t.Fatalf("got %d/%d/%v, want 200/8/nil", s, l, err)
	}
	if _, _, err := parseVarnishStatus("garbage\n"); err == nil {
		t.Fatal("a non-status line must error")
	}
}

func TestVarnishVersion(t *testing.T) {
	if v := varnishVersion("Varnish Cache CLI 1.0\nvarnish-7.4.1 revision abcdef\n"); v != "7.4.1" {
		t.Fatalf("version = %q, want 7.4.1", v)
	}
	if v := varnishVersion("no version here"); v != "" {
		t.Fatalf("version = %q, want empty", v)
	}
}

// serveVarnish writes a single CLI response (status + body) and closes.
func serveVarnish(t *testing.T, status int, body string) int {
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
		_, _ = fmt.Fprintf(c, "%-3d %-8d\n%s\n", status, len(body), body)
	}()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return port
}

func TestVarnishProbeBanner(t *testing.T) {
	port := serveVarnish(t, 200, "Varnish Cache CLI 1.0\nvarnish-7.4.1 revision abcdef\n\nType 'help' for command list.")
	res, err := varnishProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "7.4.1" {
		t.Fatalf("version = %q, want 7.4.1", res.Version)
	}
	if res.Extra["cli_status"] != "200" {
		t.Fatalf("cli_status = %q", res.Extra["cli_status"])
	}
}

func TestVarnishProbeAuthChallenge(t *testing.T) {
	port := serveVarnish(t, 107, "ixslvvxrgkjptxmcgnnsdxsvdmvfympg\n\nAuthentication required.")
	res, err := varnishProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["cli_status"] != "107" || res.Extra["auth_required"] != "true" {
		t.Fatalf("extra = %v", res.Extra)
	}
}
