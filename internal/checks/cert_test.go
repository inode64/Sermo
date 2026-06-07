package checks

import (
	"context"
	"testing"
	"time"
)

func fakeCert(s CertSample) CertSamplerFunc {
	return func(context.Context, string, string, string, bool) (CertSample, error) { return s, nil }
}

func healthyCert() CertSample {
	return CertSample{
		NotBefore:          time.Now().Add(-24 * time.Hour),
		NotAfter:           time.Now().Add(60 * 24 * time.Hour),
		SignatureAlgorithm: "SHA256-RSA",
		PublicKeyAlgorithm: "RSA",
		Issuer:             "CN=Let's Encrypt",
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
	if res.OK {
		t.Fatalf("a valid cert 60 days out should not alert: %q", res.Message)
	}
	if res.Data["days_left"].(int) < 58 {
		t.Fatalf("days_left = %v", res.Data["days_left"])
	}
}

func TestCertExpiringSoon(t *testing.T) {
	s := healthyCert()
	s.NotAfter = time.Now().Add(5 * 24 * time.Hour)
	c := certWith(s)
	c.expiresInDays = 14
	if !c.Run(context.Background()).OK {
		t.Fatal("a cert 5 days out should alert with expires_in_days=14")
	}
}

func TestCertExpiredAndNotYetValid(t *testing.T) {
	expired := healthyCert()
	expired.NotAfter = time.Now().Add(-time.Hour)
	if !certWith(expired).Run(context.Background()).OK {
		t.Fatal("an expired cert must alert")
	}
	future := healthyCert()
	future.NotBefore = time.Now().Add(time.Hour)
	if !certWith(future).Run(context.Background()).OK {
		t.Fatal("a not-yet-valid cert must alert")
	}
}

func TestCertVerifyError(t *testing.T) {
	s := healthyCert()
	s.VerifyError = "x509: certificate signed by unknown authority"
	c := certWith(s)
	if res := c.Run(context.Background()); !res.OK {
		t.Fatal("a chain verify error must alert")
	}
}

func TestCertAlgorithmChangeEdge(t *testing.T) {
	cur := healthyCert()
	c := &certCheck{base: base{name: "c"}, host: "x", port: "443", serverName: "x", verify: false, onAlgoChange: true,
		sampler: func(context.Context, string, string, string, bool) (CertSample, error) { return cur, nil }}

	if c.Run(context.Background()).OK {
		t.Fatal("first run primes and must not alert on change")
	}
	cur.SignatureAlgorithm = "ECDSA-SHA256" // algorithm changed
	if !c.Run(context.Background()).OK {
		t.Fatal("an algorithm change must alert after priming")
	}
	if c.Run(context.Background()).OK {
		t.Fatal("a stable algorithm must not keep alerting")
	}
}

func TestCertIssuerAndFingerprintChange(t *testing.T) {
	cur := healthyCert()
	c := &certCheck{base: base{name: "c"}, host: "x", port: "443", serverName: "x", verify: false,
		onIssuerChange: true, onChange: true,
		sampler: func(context.Context, string, string, string, bool) (CertSample, error) { return cur, nil }}
	c.Run(context.Background()) // prime
	cur.Issuer = "CN=Other CA"
	cur.Fingerprint = "bbbb"
	if !c.Run(context.Background()).OK {
		t.Fatal("issuer/fingerprint change must alert")
	}
}

func TestCertSamplerErrorIsNotAlert(t *testing.T) {
	c := &certCheck{base: base{name: "c"}, host: "x", port: "443", serverName: "x", verify: true,
		sampler: func(context.Context, string, string, string, bool) (CertSample, error) {
			return CertSample{}, context.DeadlineExceeded
		}}
	if c.Run(context.Background()).OK {
		t.Fatal("a sampler error must not alert (reachability is a tcp/http concern)")
	}
}

func TestBuildCertCheck(t *testing.T) {
	built, warns := Build(map[string]any{
		"api": map[string]any{"type": "cert", "host": "api.example.com", "expires_in_days": 14, "on_algorithm_change": true},
	}, Deps{CertSampler: fakeCert(healthyCert())})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("cert check should build: warns=%v built=%d", warns, len(built))
	}
	if built[0].Check.Run(context.Background()).OK {
		t.Fatal("a healthy cert should not alert")
	}
	if _, warns := Build(map[string]any{"c": map[string]any{"type": "cert"}}, Deps{}); len(warns) == 0 {
		t.Fatal("a cert check without a host should warn")
	}
}
