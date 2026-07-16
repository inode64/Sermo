package conn

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInfluxdbProbeHealth(t *testing.T) {
	host, port := serveJSON(t, "/health", `{"name":"influxdb","message":"ready","status":"pass","version":"2.7.1"}`)
	res, err := influxdbProtocol{}.Probe(context.Background(), Config{Host: host, Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "2.7.1" || res.Extra["status"] != "pass" {
		t.Fatalf("res = %+v", res)
	}
}

func TestInfluxdbProbeHealthFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"name":"influxdb","status":"fail","message":"storage unavailable"}`)
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv)
	if _, err := (influxdbProtocol{}).Probe(context.Background(), Config{Host: host, Port: port}); err == nil {
		t.Fatal("a fail health status must error")
	}
}

func TestInfluxdbProbePingFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health": // an older v1 server without /health
			http.NotFound(w, r)
		case "/ping":
			w.Header().Set("X-Influxdb-Version", "1.8.10")
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv)
	res, err := influxdbProtocol{}.Probe(context.Background(), Config{Host: host, Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "1.8.10" {
		t.Fatalf("version = %q, want 1.8.10", res.Version)
	}
}

func TestInfluxdbProbeDown(t *testing.T) {
	assertProbeRefused(t, influxdbProtocol{}, deadPort(t))
}
