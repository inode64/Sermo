package conn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRspamdVersion(t *testing.T) {
	runMapCases(t, "rspamdVersion", rspamdVersion, map[string]string{
		"Rspamd/3.8.4":            "3.8.4",
		"rspamd/3.8.4":            "3.8.4",
		"Rspamd/3.8.4 (proxy)":    "3.8.4",
		"nginx":                   "",
		"":                        "",
		"Rspamd/2.7; extra":       "2.7",
		"prefix Rspamd/1.9.0 end": "1.9.0",
	})
}

func TestRspamdProbeAgainstFakeServer(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Server", "Rspamd/3.8.4")
		_, _ = w.Write([]byte("pong\r\n"))
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv)
	res, err := rspamdProtocol{}.Probe(context.Background(), Config{Host: host, Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if gotPath != "/ping" {
		t.Fatalf("probe hit %q, want /ping", gotPath)
	}
	if res.Version != "3.8.4" {
		t.Fatalf("version = %q, want 3.8.4", res.Version)
	}
	if res.Extra["ping"] != "pong" || res.Extra["server"] != "Rspamd/3.8.4" {
		t.Fatalf("extra = %v", res.Extra)
	}
}

func TestRspamdProbeTLSSkipVerify(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Server", "Rspamd/3.9.0")
		_, _ = w.Write([]byte("pong"))
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv)
	res, err := rspamdProtocol{}.Probe(context.Background(), Config{Host: host, Port: port, TLS: "skip-verify"})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "3.9.0" {
		t.Fatalf("version = %q, want 3.9.0", res.Version)
	}
}

func TestRspamdProbeFailures(t *testing.T) {
	// Non-200 fails.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	host, port := serverHostPort(t, srv)
	if _, err := (rspamdProtocol{}).Probe(context.Background(), Config{Host: host, Port: port}); err == nil {
		t.Fatal("a 500 response must fail the probe")
	}
	srv.Close()

	// 200 but not "pong" fails (e.g. a different HTTP server on that port).
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>not rspamd</html>"))
	}))
	defer srv.Close()
	host, port = serverHostPort(t, srv)
	if _, err := (rspamdProtocol{}).Probe(context.Background(), Config{Host: host, Port: port}); err == nil {
		t.Fatal("a non-pong body must fail the probe")
	}
}
