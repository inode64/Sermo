package checks

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"strings"
	"testing"
	"time"
)

func fakeCert(s CertSample) CertSamplerFunc {
	return func(context.Context, string, string, string, bool) (CertSample, error) { return s, nil }
}

// mustSelfSigned mints a self-signed leaf certificate for tests.
func mustSelfSigned(t *testing.T, notBefore, notAfter time.Time) *x509.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test.local"},
		DNSNames:     []string{"test.local"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestCertEvaluatorChangeAndExpiry(t *testing.T) {
	var e certEvaluator
	opts := certOptions{expiresInDays: 14, onChange: true}
	now := time.Now()

	s := healthyCert() // 60 days out, fingerprint "aaaa"
	if probs, _, _ := e.evaluate(s, opts, now); len(probs) != 0 {
		t.Fatalf("first observation primes and a healthy cert must not alert: %v", probs)
	}
	s.Fingerprint = "bbbb" // changed
	if probs, _, _ := e.evaluate(s, opts, now); len(probs) != 1 || probs[0] != "certificate changed" {
		t.Fatalf("a fingerprint change must alert after priming: %v", probs)
	}

	// Expiry threshold is independent of priming.
	var e2 certEvaluator
	soon := healthyCert()
	soon.NotAfter = now.Add(5 * 24 * time.Hour)
	probs, daysLeft, hasExpiry := e2.evaluate(soon, certOptions{expiresInDays: 14}, now)
	if !hasExpiry || daysLeft > 5 || len(probs) != 1 {
		t.Fatalf("a cert 5 days out must alert with expires_in_days=14: probs=%v days=%d", probs, daysLeft)
	}
}

func TestVerifyCertChainSelfSigned(t *testing.T) {
	// A self-signed leaf does not chain to the system roots.
	leaf := mustSelfSigned(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if got := verifyCertChain(leaf, nil, leaf.Subject.CommonName); got == "" {
		t.Fatal("a self-signed cert must produce a verify error")
	}
}

func healthyCert() CertSample {
	return CertSample{
		NotBefore:          time.Now().Add(-24 * time.Hour),
		NotAfter:           time.Now().Add(60 * 24 * time.Hour),
		SignatureAlgorithm: "SHA256-RSA",
		PublicKeyAlgorithm: "RSA",
		Issuer:             "CN=Let's Encrypt",
		Subject:            "CN=api.example.com",
		SerialNumber:       "deadbeef",
		DNSNames:           []string{"api.example.com", "www.example.com"},
		Fingerprint:        "aaaa",
	}
}

func certWith(s CertSample) *certCheck {
	return &certCheck{base: base{name: "c"}, host: "x", port: "443", serverName: "x", verify: true, sampler: fakeCert(s)}
}

func TestCertHealthyNoAlert(t *testing.T) {
	c := certWith(healthyCert())
	c.expiresInDays = 14
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("a valid cert 60 days out should pass: %q", res.Message)
	}
	if res.Data["days_left"].(int) < 58 {
		t.Fatalf("days_left = %v", res.Data["days_left"])
	}
}

func TestCertDataFields(t *testing.T) {
	c := certWith(healthyCert())
	res := c.Run(context.Background())
	cases := map[string]any{
		"issuer":               "CN=Let's Encrypt",
		"subject":              "CN=api.example.com",
		"serial_number":        "deadbeef",
		"signature_algorithm":  "SHA256-RSA",
		"public_key_algorithm": "RSA",
		"fingerprint":          "aaaa",
	}
	for k, want := range cases {
		if got := res.Data[k]; got != want {
			t.Errorf("Data[%q] = %v, want %v", k, got, want)
		}
	}
	if _, ok := res.Data["not_before"].(string); !ok {
		t.Errorf("not_before missing or not a string: %v", res.Data["not_before"])
	}
	if _, ok := res.Data["not_after"].(string); !ok {
		t.Errorf("not_after missing or not a string: %v", res.Data["not_after"])
	}
	sans, ok := res.Data["dns_names"].([]string)
	if !ok || len(sans) != 2 || sans[0] != "api.example.com" {
		t.Errorf("dns_names = %v", res.Data["dns_names"])
	}
}

func TestCertExpiringSoon(t *testing.T) {
	s := healthyCert()
	s.NotAfter = time.Now().Add(5 * 24 * time.Hour)
	c := certWith(s)
	c.expiresInDays = 14
	if c.Run(context.Background()).OK {
		t.Fatal("a cert 5 days out should fail with expires_in_days=14")
	}
}

func TestCertExpiredAndNotYetValid(t *testing.T) {
	expired := healthyCert()
	expired.NotAfter = time.Now().Add(-time.Hour)
	if certWith(expired).Run(context.Background()).OK {
		t.Fatal("an expired cert must fail")
	}
	future := healthyCert()
	future.NotBefore = time.Now().Add(time.Hour)
	if certWith(future).Run(context.Background()).OK {
		t.Fatal("a not-yet-valid cert must fail")
	}
}

func TestCertVerifyError(t *testing.T) {
	s := healthyCert()
	s.VerifyError = "x509: certificate signed by unknown authority"
	c := certWith(s)
	if res := c.Run(context.Background()); res.OK {
		t.Fatal("a chain verify error must fail")
	}
}

func TestCertAlgorithmChangeEdge(t *testing.T) {
	cur := healthyCert()
	c := &certCheck{base: base{name: "c"}, host: "x", port: "443", serverName: "x", verify: false, onAlgoChange: true,
		sampler: func(context.Context, string, string, string, bool) (CertSample, error) { return cur, nil }}

	if !c.Run(context.Background()).OK {
		t.Fatal("first run primes and must pass without a change")
	}
	cur.SignatureAlgorithm = "ECDSA-SHA256" // algorithm changed
	if c.Run(context.Background()).OK {
		t.Fatal("an algorithm change must fail after priming")
	}
	if !c.Run(context.Background()).OK {
		t.Fatal("a stable algorithm must pass after the changed sample is recorded")
	}
}

func TestCertSamplerErrorIsNotAlert(t *testing.T) {
	c := &certCheck{base: base{name: "c"}, host: "x", port: "443", serverName: "x", verify: true,
		sampler: func(context.Context, string, string, string, bool) (CertSample, error) {
			return CertSample{}, context.DeadlineExceeded
		}}
	if !c.Run(context.Background()).OK {
		t.Fatal("a sampler error must pass (reachability is a tcp/http concern)")
	}
}

func TestBuildCertCheck(t *testing.T) {
	// A healthy cert passes; a cert check without a host warns.
	assertBuildThresholdFires(t, "cert",
		map[string]any{"host": "api.example.com", "expires_in_days": 14, "on_algorithm_change": true},
		Deps{CertSampler: fakeCert(healthyCert())})
}

func TestCertCheckSource(t *testing.T) {
	// A path-based check reports the path; a host-based one reports the host.
	if got := (&certCheck{path: "/etc/ssl/cert.pem"}).source(); got != "/etc/ssl/cert.pem" {
		t.Errorf("source(path) = %q, want the path", got)
	}
	if got := (&certCheck{host: "example.com"}).source(); got != "example.com" {
		t.Errorf("source(host) = %q, want the host", got)
	}
}

func TestCertMessageNoExpiry(t *testing.T) {
	// Public-key material: algorithm and bits are appended when known.
	if got := certMessage("/k.pem", CertSample{Kind: "public_key", PublicKeyAlgorithm: "RSA", KeyBits: 2048}, 0, false); got != "/k.pem: public_key, RSA 2048 bits" {
		t.Errorf("full = %q", got)
	}
	// No algorithm -> just the kind.
	if got := certMessage("/k.pem", CertSample{Kind: "private_key"}, 0, false); got != "/k.pem: private_key" {
		t.Errorf("no-algo = %q", got)
	}
	// Algorithm known but bits unknown (0) -> no bits suffix.
	if got := certMessage("/k.pem", CertSample{Kind: "public_key", PublicKeyAlgorithm: "Ed25519"}, 0, false); got != "/k.pem: public_key, Ed25519" {
		t.Errorf("no-bits = %q", got)
	}
}

func TestCertDataOptionalFields(t *testing.T) {
	full := CertSample{Kind: "certificate", Fingerprint: "fp", KeyBits: 2048, DNSNames: []string{"a.example"}}
	d := certData("src", "host.example", "/p.pem", full, 5, true)
	if d["host"] != "host.example" || d["path"] != "/p.pem" || d["key_bits"] != 2048 {
		t.Fatalf("present optional fields = %v", d)
	}
	if sans, ok := d["dns_names"].([]string); !ok || len(sans) != 1 {
		t.Fatalf("dns_names = %v", d["dns_names"])
	}
	// Minimal sample: every optional field is omitted, not emitted as a zero value.
	m := certData("src", "", "", CertSample{Kind: "certificate", Fingerprint: "fp"}, 0, false)
	for _, k := range []string{"host", "path", "key_bits", "dns_names", "days_left"} {
		if _, has := m[k]; has {
			t.Errorf("minimal sample must omit %q, got %v", k, m[k])
		}
	}
}

func TestCertIssuerChangeAlone(t *testing.T) {
	// Only onIssuerChange is enabled, so the alert must come solely from the
	// issuer comparison (not a fingerprint change masking it).
	cur := healthyCert()
	c := &certCheck{base: base{name: "c"}, host: "x", port: "443", serverName: "x", verify: false,
		onIssuerChange: true,
		sampler:        func(context.Context, string, string, string, bool) (CertSample, error) { return cur, nil }}
	c.Run(context.Background()) // prime
	cur.Issuer = "CN=Other CA"
	res := c.Run(context.Background())
	if res.OK || !strings.Contains(res.Message, "issuer changed") {
		t.Fatalf("issuer change alone must alert: %+v", res)
	}
}
