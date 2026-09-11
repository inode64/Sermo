package checks

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPRejectsTruncatedResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("ready"))
	}))
	defer srv.Close()
	result := runHTTP(t, srv, map[string]any{
		"url":         srv.URL,
		"expect_body": map[string]any{"op": "contains", "value": "ready"},
	})
	if !result.Unavailable || !strings.Contains(result.Message, "read response body") {
		t.Fatalf("truncated response = %+v, want unavailable", result)
	}
}
