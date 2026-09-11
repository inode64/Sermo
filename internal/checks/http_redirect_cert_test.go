package checks

import (
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPCertificateVerifiesFinalRedirectHost(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer origin.Close()
	check, warn := buildHTTP(t, origin, map[string]any{
		"type": "http", "url": strings.Replace(origin.URL, "127.0.0.1", "localhost", 1), "cert_verify": true,
	})
	if warn != "" {
		t.Fatal(warn)
	}
	hc := check.(*httpCheck)
	var names []string
	hc.certVerification.verify = func(_ *x509.Certificate, _ []*x509.Certificate, name string) string {
		names = append(names, name)
		return ""
	}
	result := hc.Run(t.Context())
	if !result.OK || len(names) == 0 || names[len(names)-1] != "127.0.0.1" {
		t.Fatalf("result = %+v, verification hosts = %v, want final IP host", result, names)
	}
}

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
