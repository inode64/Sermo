package checks

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
)

// selfSignedTLS builds a self-signed certificate for 127.0.0.1 for the HTTP/3
// test server.
func selfSignedTLS(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func TestHTTP3RoundTrip(t *testing.T) {
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = udp.Close() }()

	srv := &http3.Server{
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{selfSignedTLS(t)}, MinVersion: tls.VersionTLS13},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}),
	}
	go func() { _ = srv.Serve(udp) }()
	defer func() { _ = srv.Close() }()

	port := udp.LocalAddr().(*net.UDPAddr).Port
	tr := &http3.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // test server uses a self-signed cert
	defer func() { _ = tr.Close() }()

	c := &httpCheck{
		base:   base{name: "h3", timeout: 5 * time.Second},
		client: &http.Client{Transport: tr},
		url:    fmt.Sprintf("https://127.0.0.1:%d/", port),
		method: "GET",
		expect: statusMatcher{codes: []int{200}},
	}
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("HTTP/3 request should pass: %s", res.Message)
	}
	if res.Data["protocol"] != "HTTP/3.0" {
		t.Fatalf("protocol = %v, want HTTP/3.0", res.Data["protocol"])
	}
}

func TestBuildHTTP3Client(t *testing.T) {
	built, warns := Build(map[string]any{
		"a": map[string]any{"type": "http", "url": "https://example.com/", "http3": true},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("http3 check should build: warns=%v", warns)
	}
	hc := built[0].Check.(*httpCheck)
	if _, ok := hc.client.Transport.(*http3.Transport); !ok {
		t.Fatalf("expected an http3 transport, got %T", hc.client.Transport)
	}

	// http3 over a plain http:// url is rejected.
	if _, warns := Build(map[string]any{
		"a": map[string]any{"type": "http", "url": "http://example.com/", "http3": true},
	}, Deps{DefaultTimeout: time.Second}); len(warns) == 0 {
		t.Fatal("http3 with an http:// url should warn")
	}

	// http3 + proxy is rejected.
	if _, warns := Build(map[string]any{
		"a": map[string]any{"type": "http", "url": "https://example.com/", "http3": true, "proxy": "http://squid:3128"},
	}, Deps{DefaultTimeout: time.Second}); len(warns) == 0 {
		t.Fatal("http3 with a proxy should warn")
	}
}

func TestHTTPProtocolExposed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c, warn := buildHTTP(t, srv, map[string]any{"type": "http", "url": srv.URL})
	if warn != "" {
		t.Fatal(warn)
	}
	res := c.Run(context.Background())
	if res.Data["protocol"] != "HTTP/1.1" {
		t.Fatalf("protocol = %v, want HTTP/1.1", res.Data["protocol"])
	}
}
