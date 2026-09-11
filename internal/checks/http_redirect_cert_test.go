package checks

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPCertificateRejectsRedirectToPlainHTTP(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer origin.Close()
	result := runHTTP(t, origin, map[string]any{"url": origin.URL, "cert_verify": true})
	if result.OK || !strings.Contains(result.Message, "requires a TLS response") {
		t.Fatalf("redirect result = %+v, want certificate failure", result)
	}
}
