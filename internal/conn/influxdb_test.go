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
	"time"
)

func influxHostPort(t *testing.T, srv *httptest.Server) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	return host, port
}

func TestInfluxdbProbeHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"name":"influxdb","message":"ready","status":"pass","version":"2.7.1"}`)
	}))
	defer srv.Close()

	host, port := influxHostPort(t, srv)
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

	host, port := influxHostPort(t, srv)
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

	host, port := influxHostPort(t, srv)
	res, err := influxdbProtocol{}.Probe(context.Background(), Config{Host: host, Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "1.8.10" {
		t.Fatalf("version = %q, want 1.8.10", res.Version)
	}
}

func TestInfluxdbProbeDown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()
	port, _ := strconv.Atoi(portStr)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := (influxdbProtocol{}).Probe(ctx, Config{Host: "127.0.0.1", Port: port}); err == nil {
		t.Fatal("probing a down InfluxDB must error")
	}
}
