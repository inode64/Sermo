package conn

import (
	"fmt"
	"net"
	"testing"
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
