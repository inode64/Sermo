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

func promHostPort(t *testing.T, srv *httptest.Server) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	return host, port
}

func TestPrometheusProbeBuildInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/status/buildinfo" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"status":"success","data":{"version":"2.45.0","revision":"abc123"}}`)
	}))
	defer srv.Close()

	host, port := promHostPort(t, srv)
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

	host, port := promHostPort(t, srv)
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

	host, port := promHostPort(t, srv)
	res, err := prometheusProtocol{}.Probe(context.Background(), Config{Host: host, Port: port, User: "ops", Password: "p"})
	if err != nil {
		t.Fatalf("probe with basic auth: %v", err)
	}
	if res.Version != "2.50.0" {
		t.Fatalf("version = %q", res.Version)
	}
}

func TestPrometheusProbeDown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()
	port, _ := strconv.Atoi(portStr)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := (prometheusProtocol{}).Probe(ctx, Config{Host: "127.0.0.1", Port: port}); err == nil {
		t.Fatal("probing a down Prometheus must error")
	}
}
