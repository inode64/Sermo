package conn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

func TestSyncthingRegistered(t *testing.T) {
	p, ok := Lookup("syncthing")
	if !ok {
		t.Fatal("syncthing not registered")
	}
	if p.DefaultPort() != 8384 {
		t.Fatalf("default port = %d, want 8384", p.DefaultPort())
	}
	if p.RequiresUser() {
		t.Fatal("syncthing must not require a user")
	}
}

// syncthingTestServer serves the health endpoint and, for requests carrying the
// expected API key, the version endpoint.
func syncthingTestServer(apiKey string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/noauth/health":
			_, _ = w.Write([]byte(`{"status":"OK"}`))
		case "/rest/system/version":
			if apiKey == "" || r.Header.Get("X-API-Key") != apiKey {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(`{"version":"v1.27.0","os":"linux","arch":"amd64"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func probeSyncthing(t *testing.T, serverURL string, cfg Config) (Result, error) {
	t.Helper()
	u, _ := url.Parse(serverURL)
	port, _ := strconv.Atoi(u.Port())
	cfg.Host, cfg.Port = u.Hostname(), port
	return syncthingProtocol{}.Probe(context.Background(), cfg)
}

func TestSyncthingProbeHealthOnly(t *testing.T) {
	srv := syncthingTestServer("")
	defer srv.Close()
	res, err := probeSyncthing(t, srv.URL, Config{})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["health"] != "OK" {
		t.Fatalf("extra = %v", res.Extra)
	}
	if res.Version != "" {
		t.Fatalf("no API key: version should be empty, got %q", res.Version)
	}
}

func TestSyncthingProbeWithAPIKey(t *testing.T) {
	srv := syncthingTestServer("secret-key")
	defer srv.Close()
	res, err := probeSyncthing(t, srv.URL, Config{Password: "secret-key"})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "v1.27.0" {
		t.Fatalf("version = %q, want v1.27.0", res.Version)
	}
	if res.Extra["os"] != "linux" || res.Extra["arch"] != "amd64" {
		t.Fatalf("extra = %v", res.Extra)
	}
}

func TestSyncthingProbeTLSSkipVerify(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"OK"}`))
	}))
	defer srv.Close()
	res, err := probeSyncthing(t, srv.URL, Config{TLS: "skip-verify"})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["health"] != "OK" {
		t.Fatalf("extra = %v", res.Extra)
	}
}

func TestSyncthingProbeFailures(t *testing.T) {
	// A wrong API key makes the version call 403, which fails the probe.
	srv := syncthingTestServer("right-key")
	defer srv.Close()
	if _, err := probeSyncthing(t, srv.URL, Config{Password: "wrong-key"}); err == nil {
		t.Fatal("a wrong API key must fail the probe")
	}

	// status != OK fails.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"degraded"}`))
	}))
	defer bad.Close()
	if _, err := probeSyncthing(t, bad.URL, Config{}); err == nil {
		t.Fatal("a non-OK health status must fail the probe")
	}

	// non-200 on health fails.
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer down.Close()
	if _, err := probeSyncthing(t, down.URL, Config{}); err == nil {
		t.Fatal("a 503 health response must fail the probe")
	}
}
