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
	"strings"
	"sync/atomic"
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
	serverURL := startHTTP3TestServer(t)
	tr := &http3.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	defer func() { _ = tr.Close() }()

	c := &httpCheck{
		base:   base{name: "h3", timeout: 5 * time.Second},
		client: &http.Client{Transport: tr},
		url:    serverURL,
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

func TestHTTP3CertificateVerificationRunsOnce(t *testing.T) {
	built, warns := Build(map[string]any{
		"h3": map[string]any{
			"type": "http", "url": startHTTP3TestServer(t), "http3": true, "cert_verify": true,
		},
	}, Deps{DefaultTimeout: 5 * time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("Build() = %d checks, warnings %v", len(built), warns)
	}
	hc := built[0].Check.(*httpCheck)
	var calls atomic.Int32
	hc.certVerification.verify = func(*x509.Certificate, []*x509.Certificate, string) string {
		calls.Add(1)
		return ""
	}

	if result := hc.Run(t.Context()); !result.OK {
		t.Fatalf("HTTP/3 certificate result = %+v, want success from injected verifier", result)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("chain verification calls = %d, want 1", got)
	}
}

func startHTTP3TestServer(t *testing.T) string {
	t.Helper()
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = udp.Close() })

	srv := &http3.Server{
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{selfSignedTLS(t)}, MinVersion: tls.VersionTLS13},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}),
	}
	go func() { _ = srv.Serve(udp) }()
	t.Cleanup(func() { _ = srv.Close() })

	port := udp.LocalAddr().(*net.UDPAddr).Port
	return fmt.Sprintf("https://127.0.0.1:%d/", port)
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

	// HTTP/3 binds its UDP socket to the first configured interface.
	bound, warns := Build(map[string]any{
		"a": map[string]any{
			"type": "http", "url": "https://example.com/", "http3": true,
			"interface": "lo", "cert_verify": false,
		},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(bound) != 1 {
		t.Fatalf("http3 check with interface should build: warns=%v", warns)
	}
	boundHTTP := bound[0].Check.(*httpCheck)
	requestTransport := boundHTTP.client.Transport.(*http3.Transport)
	if requestTransport.Dial == nil {
		t.Fatal("HTTP/3 request transport must use the bound QUIC dialer")
	}
	certTransport := boundHTTP.certClient.Transport.(*http3.Transport)
	if certTransport.Dial == nil {
		t.Fatal("HTTP/3 certificate transport must use the bound QUIC dialer")
	}
}

func TestHTTP3InterfaceFailureDoesNotFallBack(t *testing.T) {
	built, warns := Build(map[string]any{
		"a": map[string]any{
			"type": "http", "url": startHTTP3TestServer(t), "http3": true,
			"interface": "sermo-nonexistent0", "cert_verify": false,
		},
	}, Deps{DefaultTimeout: 2 * time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("http3 check with interface should build: warns=%v", warns)
	}
	if result := built[0].Check.Run(context.Background()); result.OK {
		t.Fatal("HTTP/3 must fail instead of using the default route when interface binding fails")
	}
}

func TestHTTPCertificateInterfaceFailureDoesNotFallBack(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	built, warns := Build(map[string]any{
		"a": map[string]any{
			"type": "http", "url": server.URL,
			"interface": "sermo-nonexistent0", "cert_verify": false,
		},
	}, Deps{DefaultTimeout: 2 * time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("certificate check with interface should build: warns=%v", warns)
	}
	result := built[0].Check.Run(context.Background())
	if result.OK {
		t.Fatal("certificate inspection must fail instead of using the default route when interface binding fails")
	}
	if !strings.Contains(result.Message, "sermo-nonexistent0") {
		t.Fatalf("failure %q does not identify the rejected interface", result.Message)
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
