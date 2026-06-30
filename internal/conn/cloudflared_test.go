package conn

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

func probeCloudflared(t *testing.T, serverURL string) (Result, error) {
	t.Helper()
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return cloudflaredProtocol{}.Probe(context.Background(), Config{Host: u.Hostname(), Port: port})
}

func TestCloudflaredProbeMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = io.WriteString(w, "# HELP cloudflared_tunnel_ha_connections Current HA connections\ncloudflared_tunnel_ha_connections 4\n")
	}))
	defer srv.Close()

	res, err := probeCloudflared(t, srv.URL)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["endpoint"] != "/metrics" || res.Extra["status"] != "200" {
		t.Fatalf("extra = %v", res.Extra)
	}
}

func TestCloudflaredProbeRejectsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "cloudflared_tunnel_ha_connections 0\n")
	}))
	defer srv.Close()

	if _, err := probeCloudflared(t, srv.URL); err == nil {
		t.Fatal("non-200 metrics response must fail")
	}
}

func TestCloudflaredProbeRejectsNonCloudflaredMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "go_goroutines 12\nprocess_cpu_seconds_total 1\n")
	}))
	defer srv.Close()

	if _, err := probeCloudflared(t, srv.URL); err == nil {
		t.Fatal("metrics without a cloudflared_ series must fail")
	}
}
