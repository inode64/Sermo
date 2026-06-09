package checks

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

// serveInflux runs a fake InfluxDB /query endpoint returning body, optionally
// requiring HTTP Basic auth.
func serveInflux(t *testing.T, body string, wantUser string) (host string, port int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantUser != "" {
			u, _, ok := r.BasicAuth()
			if !ok || u != wantUser {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"error":"unauthorized"}`)
				return
			}
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	h, ps, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := strconv.Atoi(ps)
	return h, p
}

func runInflux(t *testing.T, entry map[string]any) Result {
	t.Helper()
	built, warns := Build(map[string]any{"q": entry}, Deps{DefaultTimeout: 2 * time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("influxdb-query should build: warns=%v", warns)
	}
	return built[0].Check.Run(context.Background())
}

func TestInfluxQueryLastColumn(t *testing.T) {
	// columns ["time","count"] -> the default scalar is the last column (5).
	host, port := serveInflux(t, `{"results":[{"series":[{"columns":["time","count"],"values":[[1700000000,5]]}]}]}`, "")
	res := runInflux(t, map[string]any{
		"type": "influxdb-query", "host": host, "port": port,
		"database": "telegraf", "query": "SELECT count(value) FROM cpu", "op": "<", "value": "10",
	})
	if !res.OK {
		t.Fatalf("expected OK (5 < 10): %q", res.Message)
	}
	if res.Data["result"] != "5" {
		t.Fatalf("result = %v, want 5", res.Data["result"])
	}
}

func TestInfluxQueryNamedColumn(t *testing.T) {
	host, port := serveInflux(t, `{"results":[{"series":[{"columns":["time","mean","host"],"values":[[1700000000,12.5,"node1"]]}]}]}`, "")
	res := runInflux(t, map[string]any{
		"type": "influxdb-query", "host": host, "port": port, "column": "mean",
		"database": "telegraf", "query": "SELECT mean(value),host FROM cpu", "op": ">", "value": "10",
	})
	if !res.OK || res.Data["result"] != "12.5" {
		t.Fatalf("named-column result = %v (ok=%v)", res.Data["result"], res.OK)
	}
}

func TestInfluxQueryAuthAndEmpty(t *testing.T) {
	// Basic auth required; an empty series -> no value -> not OK.
	host, port := serveInflux(t, `{"results":[{}]}`, "monitor")
	res := runInflux(t, map[string]any{
		"type": "influxdb-query", "host": host, "port": port, "user": "monitor", "password": "p",
		"database": "telegraf", "query": "SELECT mean(value) FROM cpu WHERE time > now()-1m", "op": ">", "value": "0",
	})
	if res.OK {
		t.Fatalf("an empty result should not pass: %q", res.Message)
	}
	if !strings.Contains(res.Message, "no value") {
		t.Fatalf("message = %q", res.Message)
	}
}

func TestInfluxQueryError(t *testing.T) {
	host, port := serveInflux(t, `{"results":[{"error":"database not found: nope"}]}`, "")
	res := runInflux(t, map[string]any{
		"type": "influxdb-query", "host": host, "port": port,
		"database": "nope", "query": "SELECT 1", "op": "==", "value": "1",
	})
	if res.OK || !strings.Contains(res.Message, "database not found") {
		t.Fatalf("expected a query error: %q", res.Message)
	}
}

func TestBuildInfluxCheckErrors(t *testing.T) {
	cases := []map[string]any{
		{"type": "influxdb-query", "database": "d", "op": "<", "value": "1"},                       // no query
		{"type": "influxdb-query", "query": "SELECT 1", "op": "<", "value": "1"},                   // no database
		{"type": "influxdb-query", "database": "d", "query": "SELECT 1", "value": "1"},             // no/blank op
		{"type": "influxdb-query", "database": "d", "query": "SELECT 1", "op": "~~", "value": "1"}, // bad op
		{"type": "influxdb-query", "database": "d", "query": "SELECT 1", "op": "<"},                // no value
	}
	for i, entry := range cases {
		if _, warns := Build(map[string]any{"q": entry}, Deps{DefaultTimeout: time.Second}); len(warns) == 0 {
			t.Fatalf("case %d should warn: %v", i, entry)
		}
	}
}
