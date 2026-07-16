package conn

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUnifiProbeStatus(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"meta":{"rc":"ok","server_version":"7.5.187","uuid":"abc-123"},"data":[]}`)
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv)
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

	host, port := serverHostPort(t, srv)
	if _, err := (unifiProtocol{}).Probe(context.Background(), Config{Host: host, Port: port}); err == nil {
		t.Fatal("a non-ok rc must error")
	}
}

func TestUnifiProbeVerifyRejectsSelfSigned(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"meta":{"rc":"ok"}}`)
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv)
	// tls: true requires a valid certificate; the test server's is self-signed.
	if _, err := (unifiProtocol{}).Probe(context.Background(), Config{Host: host, Port: port, TLS: "true"}); err == nil {
		t.Fatal("tls: true must reject the self-signed certificate")
	}
}
