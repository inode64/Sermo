package conn

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestUnifiRegistered(t *testing.T) {
	for _, name := range []string{"unifi", "unifi-controller", "unifi-network"} {
		p, ok := Lookup(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if p.DefaultPort() != 8443 {
			t.Fatalf("%s default port = %d, want 8443", name, p.DefaultPort())
		}
		if p.RequiresUser() {
			t.Fatalf("%s must not require a user", name)
		}
	}
}

func unifiTestHostPort(t *testing.T, srv *httptest.Server) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	return host, port
}

func TestUnifiProbeStatus(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"meta":{"rc":"ok","server_version":"7.5.187","uuid":"abc-123"},"data":[]}`)
	}))
	defer srv.Close()

	host, port := unifiTestHostPort(t, srv)
	res, err := unifiProtocol{}.Probe(context.Background(), Config{Host: host, Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "7.5.187" {
		t.Fatalf("version = %q, want 7.5.187", res.Version)
	}
	if res.Extra["uuid"] != "abc-123" || res.Extra["rc"] != "ok" {
		t.Fatalf("extra = %v", res.Extra)
	}
}

func TestUnifiProbeNotOK(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"meta":{"rc":"error"}}`)
	}))
	defer srv.Close()

	host, port := unifiTestHostPort(t, srv)
	if _, err := (unifiProtocol{}).Probe(context.Background(), Config{Host: host, Port: port}); err == nil {
		t.Fatal("a non-ok rc must error")
	}
}

func TestUnifiProbeVerifyRejectsSelfSigned(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"meta":{"rc":"ok"}}`)
	}))
	defer srv.Close()

	host, port := unifiTestHostPort(t, srv)
	// tls: true requires a valid certificate; the test server's is self-signed.
	if _, err := (unifiProtocol{}).Probe(context.Background(), Config{Host: host, Port: port, TLS: "true"}); err == nil {
		t.Fatal("tls: true must reject the self-signed certificate")
	}
}
