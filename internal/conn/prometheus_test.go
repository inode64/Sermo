package conn

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPrometheusProbeBuildInfo(t *testing.T) {
	host, port := serveJSON(t, "/api/v1/status/buildinfo", `{"status":"success","data":{"version":"2.45.0","revision":"abc123"}}`)
	res, err := prometheusProtocol{}.Probe(context.Background(), Config{Host: host, Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "2.45.0" || res.Extra["revision"] != "abc123" {
		t.Fatalf("res = %+v", res)
	}
}

func TestPrometheusProbeHealthyFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/status/buildinfo": // endpoint disabled / older server
			http.NotFound(w, r)
		case "/-/healthy":
			_, _ = io.WriteString(w, "Prometheus Server is Healthy.\n")
		}
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv)
	if _, err := (prometheusProtocol{}).Probe(context.Background(), Config{Host: host, Port: port}); err != nil {
		t.Fatalf("probe should fall back to /-/healthy: %v", err)
	}
}

func TestPrometheusProbeBasicAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, _, ok := r.BasicAuth(); !ok || u != "ops" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"status":"success","data":{"version":"2.50.0"}}`)
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv)
	res, err := prometheusProtocol{}.Probe(context.Background(), Config{Host: host, Port: port, User: "ops", Password: "p"})
	if err != nil {
		t.Fatalf("probe with basic auth: %v", err)
	}
	if res.Version != "2.50.0" {
		t.Fatalf("version = %q", res.Version)
	}
}

func TestPrometheusProbeDown(t *testing.T) {
	assertProbeRefused(t, prometheusProtocol{}, deadPort(t))
}
