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
	// A configured database is carried into the result data.
	if res.Data["database"] != "telegraf" {
		t.Fatalf("data database = %v, want telegraf", res.Data["database"])
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

// serveFlux runs a fake InfluxDB 2.x POST /api/v2/query endpoint requiring the
// token and org, returning csvBody (annotated CSV).
func serveFlux(t *testing.T, csvBody, wantToken, wantOrg string) (host string, port int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/query" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Token "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"message":"unauthorized access"}`)
			return
		}
		if r.URL.Query().Get("org") != wantOrg {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"message":"organization not found"}`)
			return
		}
		_, _ = io.WriteString(w, csvBody)
	}))
	t.Cleanup(srv.Close)
	h, ps, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := strconv.Atoi(ps)
	return h, p
}

func TestInfluxFluxQuery(t *testing.T) {
	csvBody := "#datatype,string,long,double\n" +
		",result,table,_value\n" +
		",,0,42.5\n"
	host, port := serveFlux(t, csvBody, "tok123", "myorg")
	res := runInflux(t, map[string]any{
		"type": "influxdb-query", "host": host, "port": port, "language": "flux",
		"org": "myorg", "token": "tok123",
		"query": `from(bucket:"telegraf") |> range(start:-5m) |> mean()`,
		"op":    "<", "value": "80",
	})
	if !res.OK {
		t.Fatalf("expected OK (42.5 < 80): %q", res.Message)
	}
	if res.Data["result"] != "42.5" || res.Data["language"] != "flux" {
		t.Fatalf("data = %v", res.Data)
	}
	// A configured org is carried into the result data.
	if res.Data["org"] != "myorg" {
		t.Fatalf("data org = %v, want myorg", res.Data["org"])
	}
}

func TestInfluxFluxEmptyAndAuth(t *testing.T) {
	// Header but no data row -> no value.
	host, port := serveFlux(t, ",result,table,_value\n", "tok", "org")
	res := runInflux(t, map[string]any{
		"type": "influxdb-query", "host": host, "port": port, "language": "flux",
		"org": "org", "token": "tok", "query": "from(bucket:\"b\")", "op": ">", "value": "0",
	})
	if res.OK || !strings.Contains(res.Message, "no value") {
		t.Fatalf("empty flux result should not pass: %q", res.Message)
	}

	// Wrong token -> 401 surfaces the JSON message.
	res = runInflux(t, map[string]any{
		"type": "influxdb-query", "host": host, "port": port, "language": "flux",
		"org": "org", "token": "wrong", "query": "from(bucket:\"b\")", "op": ">", "value": "0",
	})
	if res.OK || !strings.Contains(res.Message, "unauthorized") {
		t.Fatalf("bad token should fail with the server message: %q", res.Message)
	}
}

func TestBuildInfluxCheckErrors(t *testing.T) {
	cases := []map[string]any{
		{"type": "influxdb-query", "database": "d", "op": "<", "value": "1"},                                     // no query
		{"type": "influxdb-query", "query": "SELECT 1", "op": "<", "value": "1"},                                 // influxql without database
		{"type": "influxdb-query", "database": "d", "query": "SELECT 1", "value": "1"},                           // no/blank op
		{"type": "influxdb-query", "database": "d", "query": "SELECT 1", "op": "~~", "value": "1"},               // bad op
		{"type": "influxdb-query", "database": "d", "query": "SELECT 1", "op": "<"},                              // no value
		{"type": "influxdb-query", "database": "d", "query": "SELECT 1", "op": "<", "value": "many"},             // non-numeric ordering value
		{"type": "influxdb-query", "database": "d", "query": "SELECT 1", "op": "=~", "value": "["},               // bad regex
		{"type": "influxdb-query", "language": "flux", "token": "t", "query": "from()", "op": "<", "value": "1"}, // flux without org
		{"type": "influxdb-query", "language": "flux", "org": "o", "query": "from()", "op": "<", "value": "1"},   // flux without token
		{"type": "influxdb-query", "language": "promql", "database": "d", "query": "x", "op": "<", "value": "1"}, // bad language
	}
	for i, entry := range cases {
		if _, warns := Build(map[string]any{"q": entry}, Deps{DefaultTimeout: time.Second}); len(warns) == 0 {
			t.Fatalf("case %d should warn: %v", i, entry)
		}
	}
}

func TestInfluxErrorBody(t *testing.T) {
	// 1.x reports `error`, 2.x reports `message`; anything else falls back to raw.
	if got := influxErrorBody([]byte(`{"error":"boom"}`)); got != "boom" {
		t.Errorf("1.x error = %q, want boom", got)
	}
	if got := influxErrorBody([]byte(`{"message":"oops"}`)); got != "oops" {
		t.Errorf("2.x message = %q, want oops", got)
	}
	if got := influxErrorBody([]byte("plain text")); got != "plain text" {
		t.Errorf("non-JSON body = %q, want the trimmed raw body", got)
	}
}

func TestInfluxFluxColumnIndexBounds(t *testing.T) {
	// _value at the very first column (index 0) is valid (idx < 0, not <= 0).
	host, port := serveFlux(t, "#datatype,double,string,long\n_value,result,table\n5,_result,0\n", "t", "o")
	res := runInflux(t, map[string]any{
		"type": "influxdb-query", "host": host, "port": port, "language": "flux",
		"org": "o", "token": "t", "query": "from()", "op": "<", "value": "10",
	})
	if !res.OK {
		t.Fatalf("_value at index 0 must resolve: %q", res.Message)
	}
	// A header longer than the data row must error cleanly, not index out of range.
	host2, port2 := serveFlux(t, "#datatype,string,long,double\n,result,table,_value\n,,0\n", "t", "o")
	res2 := runInflux(t, map[string]any{
		"type": "influxdb-query", "host": host2, "port": port2, "language": "flux",
		"org": "o", "token": "t", "query": "from()", "op": "<", "value": "10",
	})
	if res2.OK || !strings.Contains(res2.Message, "not found") {
		t.Fatalf("missing value column must fail with not-found, got %q", res2.Message)
	}
}

func TestInfluxConnConfigHostDefault(t *testing.T) {
	if got := influxConnConfig(map[string]any{}).Host; got != "127.0.0.1" {
		t.Errorf("default host = %q, want 127.0.0.1", got)
	}
	if got := influxConnConfig(map[string]any{"host": "tsdb.internal"}).Host; got != "tsdb.internal" {
		t.Errorf("explicit host = %q, want tsdb.internal", got)
	}
}
