package checks

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func certForPath(path string) *certCheck {
	return &certCheck{base: base{name: "c"}, path: path}
}

func makeCertPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(12345),
		Subject:      pkix.Name{CommonName: "api.example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(60 * 24 * time.Hour),
		DNSNames:     []string{"api.example.com"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func makeCSRPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: "csr.example.com"},
		DNSNames: []string{"csr.example.com"},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func makeRSAKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func makeECKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

func makePKCS8PEM(t *testing.T) []byte {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func makePKIXPubPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

func makeOpenSSHPrivPEM(t *testing.T) []byte {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(block)
}

func makeOpenSSHPub(t *testing.T) []byte {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return ssh.MarshalAuthorizedKey(signer.PublicKey())
}

func TestCertFileFormats(t *testing.T) {
	cases := []struct {
		name     string
		gen      func(*testing.T) []byte
		wantKind string
		wantAlgo string // expected public_key_algorithm
		expiry   bool
	}{
		{"certificate", makeCertPEM, "certificate", "RSA", true},
		{"csr", makeCSRPEM, "certificate_request", "RSA", false},
		{"rsa_pkcs1", makeRSAKeyPEM, "private_key", "RSA", false},
		{"ec", makeECKeyPEM, "private_key", "ECDSA", false},
		{"pkcs8_ed25519", makePKCS8PEM, "private_key", "Ed25519", false},
		{"pkix_public", makePKIXPubPEM, "public_key", "RSA", false},
		{"openssh_private", makeOpenSSHPrivPEM, "openssh_private_key", "Ed25519", false},
		{"openssh_public", makeOpenSSHPub, "openssh_public_key", "ssh-ed25519", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTemp(t, tc.name, tc.gen(t))
			res := certForPath(path).Run(context.Background())
			if res.OK {
				t.Fatalf("healthy %s should not alert: %q", tc.name, res.Message)
			}
			if got := res.Data["kind"]; got != tc.wantKind {
				t.Errorf("kind = %v, want %v", got, tc.wantKind)
			}
			if got := res.Data["public_key_algorithm"]; got != tc.wantAlgo {
				t.Errorf("public_key_algorithm = %v, want %v", got, tc.wantAlgo)
			}
			if fp, ok := res.Data["fingerprint"].(string); !ok || fp == "" {
				t.Errorf("fingerprint missing: %v", res.Data["fingerprint"])
			}
			if tc.expiry {
				if _, ok := res.Data["days_left"].(int); !ok {
					t.Errorf("certificate should expose days_left: %v", res.Data["days_left"])
				}
			} else if _, present := res.Data["days_left"]; present {
				t.Errorf("non-certificate should not expose days_left")
			}
		})
	}
}

func TestCertFileMissingIsAlert(t *testing.T) {
	res := certForPath(filepath.Join(t.TempDir(), "nope.pem")).Run(context.Background())
	if !res.OK {
		t.Fatalf("a missing file must alert: %q", res.Message)
	}
}

func TestCertFileGarbageIsAlert(t *testing.T) {
	path := writeTemp(t, "garbage", []byte("not a certificate or key\n"))
	res := certForPath(path).Run(context.Background())
	if !res.OK {
		t.Fatalf("unparseable material must alert: %q", res.Message)
	}
}

func TestBuildCertPathSource(t *testing.T) {
	built, warns := Build(map[string]any{
		"f": map[string]any{"type": "cert", "path": "/etc/ssl/cert.pem"},
	}, Deps{})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("path cert check should build: warns=%v built=%d", warns, len(built))
	}
	if _, warns := Build(map[string]any{
		"f": map[string]any{"type": "cert", "host": "x", "path": "/etc/ssl/cert.pem"},
	}, Deps{}); len(warns) == 0 {
		t.Fatal("host and path together must warn (mutually exclusive)")
	}
}
