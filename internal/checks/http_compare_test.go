package checks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// httpCompareServer serves fixed responses for the comparison tests: a status
// code, a body and an optional JSON document keyed by path.
func httpCompareServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/num":
			_, _ = w.Write([]byte("42\n"))
		case "/text":
			_, _ = w.Write([]byte("OK\n"))
		case "/ver":
			_, _ = w.Write([]byte("v1.2.3"))
		case "/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":"v9.0.1","count":7}`))
		case "/teapot":
			w.WriteHeader(http.StatusTeapot) // 418
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func runHTTP(t *testing.T, srv *httptest.Server, entry map[string]any) Result {
	t.Helper()
	entry["type"] = "http"
	c, warn := buildHTTP(t, srv, entry)
	if warn != "" {
		t.Fatalf("build warn: %s", warn)
	}
	return c.Run(context.Background())
}

func TestHTTPMethods(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
		res := runHTTP(t, srv, map[string]any{"url": srv.URL, "method": m})
		if !res.OK {
			t.Fatalf("%s should pass: %s", m, res.Message)
		}
		if got != m {
			t.Fatalf("server saw method %q, want %q", got, m)
		}
	}

	// A lowercase method is normalized to uppercase before the request.
	res := runHTTP(t, srv, map[string]any{"url": srv.URL, "method": "delete"})
	if !res.OK || got != "DELETE" {
		t.Fatalf("lowercase method should normalize to DELETE: got %q ok=%v", got, res.OK)
	}

	// A PUT carrying a body still works (the body is sent for any method).
	got = ""
	if res := runHTTP(t, srv, map[string]any{"url": srv.URL, "method": "PUT", "body": "payload"}); !res.OK || got != "PUT" {
		t.Fatalf("PUT with body should pass: got %q ok=%v", got, res.OK)
	}
}

func TestHTTPBodyOperators(t *testing.T) {
	srv := httpCompareServer(t)
	defer srv.Close()

	// Numeric body comparison (trimmed): 42 > 10 passes, 42 > 100 fails.
	if res := runHTTP(t, srv, map[string]any{"url": srv.URL + "/num", "expect_body": map[string]any{"op": ">", "value": "10"}}); !res.OK {
		t.Fatalf("42 > 10 should pass: %s", res.Message)
	}
	if res := runHTTP(t, srv, map[string]any{"url": srv.URL + "/num", "expect_body": map[string]any{"op": ">", "value": "100"}}); res.OK {
		t.Fatalf("42 > 100 should fail")
	}
	// String equality (body trimmed of the trailing newline).
	if res := runHTTP(t, srv, map[string]any{"url": srv.URL + "/text", "expect_body": map[string]any{"op": "==", "value": "OK"}}); !res.OK {
		t.Fatalf(`body == "OK" should pass: %s`, res.Message)
	}
	if res := runHTTP(t, srv, map[string]any{"url": srv.URL + "/text", "expect_body": map[string]any{"op": "!=", "value": "OK"}}); res.OK {
		t.Fatalf(`body != "OK" should fail`)
	}
	// Regex on the body.
	if res := runHTTP(t, srv, map[string]any{"url": srv.URL + "/ver", "expect_body": map[string]any{"op": "=~", "value": `^v[0-9]+\.[0-9]+`}}); !res.OK {
		t.Fatalf("regex on body should match: %s", res.Message)
	}
	if res := runHTTP(t, srv, map[string]any{"url": srv.URL + "/ver", "expect_body": map[string]any{"op": "contains", "value": "1.2"}}); !res.OK {
		t.Fatalf("contains expect_body should pass: %s", res.Message)
	}
}

func TestHTTPBodyStringExpectationRejected(t *testing.T) {
	srv := httpCompareServer(t)
	defer srv.Close()

	_, warn := buildHTTP(t, srv, map[string]any{
		"type":        "http",
		"url":         srv.URL + "/ver",
		"expect_body": "1.2",
	})
	if warn != `check "h": http expect_body must be an {op, value} mapping` {
		t.Fatalf("warn = %q, want expect_body shape error", warn)
	}
}

func TestHTTPStatusOperator(t *testing.T) {
	srv := httpCompareServer(t)
	defer srv.Close()

	// 418 < 500 passes.
	if res := runHTTP(t, srv, map[string]any{"url": srv.URL + "/teapot", "expect_status": map[string]any{"op": "<", "value": "500"}}); !res.OK {
		t.Fatalf("418 < 500 should pass: %s", res.Message)
	}
	// 418 >= 500 fails.
	if res := runHTTP(t, srv, map[string]any{"url": srv.URL + "/teapot", "expect_status": map[string]any{"op": ">=", "value": "500"}}); res.OK {
		t.Fatalf("418 >= 500 should fail")
	}
}

func TestHTTPLatency(t *testing.T) {
	srv := httpCompareServer(t)
	defer srv.Close()

	// A generous ceiling passes and exposes data.
	res := runHTTP(t, srv, map[string]any{"url": srv.URL + "/text", "expect_latency": map[string]any{"op": "<", "value": "100000"}})
	if !res.OK {
		t.Fatalf("latency under 100s should pass: %s", res.Message)
	}
	if _, ok := res.Data["latency_ms"]; !ok {
		t.Fatalf("data should carry latency_ms: %v", res.Data)
	}
	if res.Data["status"] != 200 {
		t.Fatalf("data should carry status: %v", res.Data)
	}
	// latency < 0 is impossible -> deterministic failure.
	if res := runHTTP(t, srv, map[string]any{"url": srv.URL + "/text", "expect_latency": map[string]any{"op": "<", "value": "0"}}); res.OK {
		t.Fatalf("latency < 0 must fail")
	}
}

func TestHTTPJSONRegex(t *testing.T) {
	srv := httpCompareServer(t)
	defer srv.Close()

	if res := runHTTP(t, srv, map[string]any{
		"url":         srv.URL + "/json",
		"expect_json": map[string]any{"version": map[string]any{"op": "=~", "value": `^v[0-9]+\.`}},
	}); !res.OK {
		t.Fatalf("json regex on version should match: %s", res.Message)
	}
	if res := runHTTP(t, srv, map[string]any{
		"url":         srv.URL + "/json",
		"expect_json": map[string]any{"version": map[string]any{"op": "=~", "value": `^x`}},
	}); res.OK {
		t.Fatalf("json regex that does not match should fail")
	}
	// Sanity: the JSON document really is what we expect.
	var doc map[string]any
	resp, _ := srv.Client().Get(srv.URL + "/json")
	_ = json.NewDecoder(resp.Body).Decode(&doc)
	_ = resp.Body.Close()
	if doc["count"].(float64) != 7 {
		t.Fatalf("unexpected fixture: %v", doc)
	}
}
