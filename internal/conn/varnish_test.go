package conn

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

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
	return serveOnce(t, func(c net.Conn) {
		_, _ = fmt.Fprintf(c, "%-3d %-8d\n%s\n", status, len(body), body)
	})
}

func TestVarnishProbeBanner(t *testing.T) {
	port := serveVarnish(t, 200, "Varnish Cache CLI 1.0\nvarnish-7.4.1 revision abcdef\n\nType 'help' for command list.")
	assertProbeVersion(t, varnishProtocol{}, port, "7.4.1", "cli_status", "200")
}

func TestVarnishProbeAuthChallenge(t *testing.T) {
	port := serveVarnish(t, 107, "ixslvvxrgkjptxmcgnnsdxsvdmvfympg\n\nAuthentication required.")
	assertProbeExtras(t, varnishProtocol{}, port, map[string]string{"cli_status": "107", "auth_required": "true"})
}

func TestVarnishProbeRejectsCLIFailures(t *testing.T) {
	tests := []struct {
		name  string
		reply string
	}{
		{name: "CLIS_COMMS", reply: "400 7       \nbad cmd\n"},
		{name: "CLIS_CLOSE", reply: "500 7       \nclosing\n"},
		{name: "truncated body", reply: "200 64      \nVarnish Cache CLI 1.0\n"},
		{name: "oversized body", reply: "200 99999999\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := serveOnce(t, func(c net.Conn) { _, _ = io.WriteString(c, tt.reply) })
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if _, err := (varnishProtocol{}).Probe(ctx, Config{Host: "127.0.0.1", Port: port}); err == nil {
				t.Fatalf("reply %q must fail the probe", tt.reply)
			}
		})
	}
}
