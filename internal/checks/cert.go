package checks

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// CertSample is one observation of TLS material — a leaf certificate read from a
// live endpoint, or a certificate/request/key parsed from a local file. Fields
// that do not apply to the material's kind are left zero (e.g. a private key has
// no NotAfter or Issuer).
type CertSample struct {
	Kind               string // certificate, certificate_request, private_key, public_key, openssh_private_key, openssh_public_key, …
	NotBefore          time.Time
	NotAfter           time.Time
	SignatureAlgorithm string
	PublicKeyAlgorithm string
	KeyBits            int // key size in bits when known, else 0
	Issuer             string
	Subject            string
	SerialNumber       string   // hex
	DNSNames           []string // subject alternative names
	Fingerprint        string   // SHA-256 of the DER/raw material, hex
	VerifyError        string   // chain/hostname/validity error when verify requested, else ""
}

// CertSamplerFunc fetches a TLS endpoint's leaf certificate. Injected for tests;
// the default dials the host and inspects the presented chain.
type CertSamplerFunc func(ctx context.Context, host, port, serverName string, verify bool) (CertSample, error)

// certOptions configures which certificate conditions raise a problem.
type certOptions struct {
	expiresInDays  int
	verify         bool
	onAlgoChange   bool
	onIssuerChange bool
	onChange       bool
}

// certEvaluator turns a CertSample into the problems it represents under a set
// of certOptions. It is stateful for change detection — it remembers the
// previous sample's algorithm, issuer and fingerprint — so a change condition
// only fires from the second observation onward.
type certEvaluator struct {
	primed     bool
	lastAlgo   string
	lastIssuer string
	lastFP     string
}

// evaluate reports the problems for sample s under opts at time now, plus the
// days until expiry and whether the material has an expiry at all.
func (e *certEvaluator) evaluate(s CertSample, opts certOptions, now time.Time) (problems []string, daysLeft int, hasExpiry bool) {
	hasExpiry = !s.NotAfter.IsZero()
	if hasExpiry {
		daysLeft = int(s.NotAfter.Sub(now).Hours() / 24)
		switch {
		case now.After(s.NotAfter):
			problems = append(problems, "expired")
		case now.Before(s.NotBefore):
			problems = append(problems, "not yet valid")
		case opts.expiresInDays > 0 && daysLeft < opts.expiresInDays:
			problems = append(problems, fmt.Sprintf("expires in %d days", daysLeft))
		}
	}
	if opts.verify && s.VerifyError != "" {
		problems = append(problems, "chain: "+s.VerifyError)
	}
	if !e.primed {
		e.primed = true
	} else {
		if opts.onAlgoChange && s.SignatureAlgorithm != e.lastAlgo {
			problems = append(problems, "signature algorithm "+e.lastAlgo+" -> "+s.SignatureAlgorithm)
		}
		if opts.onIssuerChange && s.Issuer != e.lastIssuer {
			problems = append(problems, "issuer changed")
		}
		if opts.onChange && s.Fingerprint != e.lastFP {
			problems = append(problems, "certificate changed")
		}
	}
	e.lastAlgo, e.lastIssuer, e.lastFP = s.SignatureAlgorithm, s.Issuer, s.Fingerprint
	return problems, daysLeft, hasExpiry
}

// certCheck inspects TLS material. It is health-style (OK==true means the
// material is acceptable). The material comes from a live TLS endpoint (host) or a local file
// (path); exactly one is set. A certificate alerts when it is expiring within
// expiresInDays, already expired or not yet valid, fails chain/hostname
// verification (verify, network only), or — between cycles — its signature
// algorithm, issuer or fingerprint changed. Non-expiring material (keys, CSRs)
// alerts only on change, or when the file is missing/unreadable/unparseable. It
// is stateful for the change detection (a pointer), so change conditions work
// when built once (a host watch); as a per-service check only the level
// conditions apply.
type certCheck struct {
	base
	host           string
	port           string
	serverName     string
	path           string
	expiresInDays  int
	onAlgoChange   bool
	onIssuerChange bool
	onChange       bool
	verify         bool
	sampler        CertSamplerFunc

	eval certEvaluator
}

// source is the human-readable origin of the material (file path or host).
func (c *certCheck) source() string {
	if c.path != "" {
		return c.path
	}
	return c.host
}

func (c *certCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	var s CertSample
	if c.path != "" {
		// File source: a missing/unreadable/unparseable file is a configuration
		// problem, so it is an alert (unlike a transient network error below).
		data, err := os.ReadFile(c.path) //nolint:gosec // operator-configured path from check config
		if err != nil {
			return c.result(false, fmt.Sprintf("%s: %v", c.path, err), start)
		}
		parsed, err := parseCertMaterial(data)
		if err != nil {
			return c.result(false, fmt.Sprintf("%s: %v", c.path, err), start)
		}
		s = parsed
	} else {
		sampler := c.sampler
		if sampler == nil {
			sampler = defaultCertSampler
		}
		sampled, err := sampler(ctx, c.host, c.port, c.serverName, c.verify)
		if err != nil {
			// Cannot retrieve the certificate (network/TLS error): this probe is
			// quiet here — use a tcp/http check for reachability.
			return c.result(true, fmt.Sprintf("cert %s:%s: %v", c.host, c.port, err), start)
		}
		s = sampled
	}

	problems, daysLeft, hasExpiry := c.eval.evaluate(s, certOptions{
		expiresInDays:  c.expiresInDays,
		verify:         c.verify,
		onAlgoChange:   c.onAlgoChange,
		onIssuerChange: c.onIssuerChange,
		onChange:       c.onChange,
	}, time.Now())

	healthy := len(problems) == 0
	src := c.source()
	msg := certMessage(src, s, daysLeft, hasExpiry)
	if !healthy {
		msg = src + ": " + strings.Join(problems, "; ")
	}
	res := c.result(healthy, msg, start)
	res.Data = certData(c.source(), c.host, c.path, s, daysLeft, hasExpiry)
	return res
}

// certMessage builds the healthy (no-alert) summary line for the material.
func certMessage(src string, s CertSample, daysLeft int, hasExpiry bool) string {
	if hasExpiry {
		return fmt.Sprintf("%s: valid %d days (until %s), %s, issuer %s", src, daysLeft, s.NotAfter.Format("2006-01-02"), s.SignatureAlgorithm, s.Issuer)
	}
	msg := fmt.Sprintf("%s: %s", src, s.Kind)
	if s.PublicKeyAlgorithm != "" {
		msg += ", " + s.PublicKeyAlgorithm
		if s.KeyBits > 0 {
			msg += fmt.Sprintf(" %d bits", s.KeyBits)
		}
	}
	return msg
}

// certData assembles the Result data map, including only fields that apply to the
// material's kind.
func certData(source, host, path string, s CertSample, daysLeft int, hasExpiry bool) map[string]any {
	data := map[string]any{
		"kind":        s.Kind,
		"source":      source,
		"fingerprint": s.Fingerprint,
	}
	if host != "" {
		data["host"] = host
	}
	if path != "" {
		data["path"] = path
	}
	if s.SignatureAlgorithm != "" {
		data["signature_algorithm"] = s.SignatureAlgorithm
	}
	if s.PublicKeyAlgorithm != "" {
		data["public_key_algorithm"] = s.PublicKeyAlgorithm
	}
	if s.KeyBits > 0 {
		data["key_bits"] = s.KeyBits
	}
	if s.Subject != "" {
		data["subject"] = s.Subject
	}
	if s.Issuer != "" {
		data["issuer"] = s.Issuer
	}
	if s.SerialNumber != "" {
		data["serial_number"] = s.SerialNumber
	}
	if len(s.DNSNames) > 0 {
		data["dns_names"] = s.DNSNames
	}
	if hasExpiry {
		data["days_left"] = daysLeft
		data["value"] = daysLeft
		data["not_before"] = s.NotBefore.Format(time.RFC3339)
		data["not_after"] = s.NotAfter.Format(time.RFC3339)
	}
	return data
}

// parseCertMaterial inspects a file's bytes and recognises the common TLS/SSH
// material formats: PEM certificate, certificate request (CSR), PKCS#1/EC/PKCS#8
// private keys, PKIX public key, OpenSSH private key, and OpenSSH public key
// (authorized_keys line). Unknown PEM blocks (e.g. DH PARAMETERS) are reported by
// kind with a fingerprint. It returns an error when nothing is recognised.
func parseCertMaterial(data []byte) (CertSample, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		// Not PEM — try an OpenSSH authorized_keys public key line.
		if pub, _, _, _, err := ssh.ParseAuthorizedKey(data); err == nil {
			return CertSample{
				Kind:               "openssh_public_key",
				PublicKeyAlgorithm: pub.Type(),
				Fingerprint:        ssh.FingerprintSHA256(pub),
			}, nil
		}
		return CertSample{}, errors.New("unrecognized certificate/key material")
	}

	sum := sha256.Sum256(block.Bytes)
	fp := hex.EncodeToString(sum[:])

	switch block.Type {
	case "CERTIFICATE":
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return CertSample{}, err
		}
		return certSampleFromCert(cert), nil
	case "CERTIFICATE REQUEST", "NEW CERTIFICATE REQUEST":
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil {
			return CertSample{}, err
		}
		algo, bits := keyAlgoBits(csr.PublicKey)
		return CertSample{
			Kind:               "certificate_request",
			SignatureAlgorithm: csr.SignatureAlgorithm.String(),
			PublicKeyAlgorithm: algo,
			KeyBits:            bits,
			Subject:            csr.Subject.String(),
			DNSNames:           csr.DNSNames,
			Fingerprint:        fp,
		}, nil
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return CertSample{}, err
		}
		return privateKeySample("private_key", key, fp), nil
	case "EC PRIVATE KEY":
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return CertSample{}, err
		}
		return privateKeySample("private_key", key, fp), nil
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return CertSample{}, err
		}
		return privateKeySample("private_key", key, fp), nil
	case "PUBLIC KEY":
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return CertSample{}, err
		}
		algo, bits := keyAlgoBits(key)
		return CertSample{
			Kind:               "public_key",
			PublicKeyAlgorithm: algo,
			KeyBits:            bits,
			Fingerprint:        fp,
		}, nil
	case "OPENSSH PRIVATE KEY":
		key, err := ssh.ParseRawPrivateKey(data)
		if err != nil {
			return CertSample{}, err
		}
		return privateKeySample("openssh_private_key", key, fp), nil
	default:
		// Unknown PEM block (e.g. DH PARAMETERS): report what we can.
		return CertSample{
			Kind:        strings.ToLower(strings.ReplaceAll(block.Type, " ", "_")),
			Fingerprint: fp,
		}, nil
	}
}

// privateKeySample builds a CertSample for a parsed private key of the given kind.
func privateKeySample(kind string, key any, fp string) CertSample {
	algo, bits := keyAlgoBits(key)
	return CertSample{
		Kind:               kind,
		PublicKeyAlgorithm: algo,
		KeyBits:            bits,
		Fingerprint:        fp,
	}
}

// keyAlgoBits reports the public-key algorithm name and key size in bits for a
// parsed public or private key. Unknown key types yield ("", 0).
func keyAlgoBits(key any) (string, int) {
	switch k := key.(type) {
	case *rsa.PrivateKey:
		return "RSA", k.N.BitLen()
	case *rsa.PublicKey:
		return "RSA", k.N.BitLen()
	case *ecdsa.PrivateKey:
		return "ECDSA", k.Curve.Params().BitSize
	case *ecdsa.PublicKey:
		return "ECDSA", k.Curve.Params().BitSize
	case ed25519.PrivateKey, *ed25519.PrivateKey, ed25519.PublicKey, *ed25519.PublicKey:
		return "Ed25519", 256
	default:
		return "", 0
	}
}

// certSampleFromCert populates a CertSample from a parsed x509 certificate.
func certSampleFromCert(leaf *x509.Certificate) CertSample {
	sum := sha256.Sum256(leaf.Raw)
	var serial string
	if leaf.SerialNumber != nil {
		serial = leaf.SerialNumber.Text(16)
	}
	_, bits := keyAlgoBits(leaf.PublicKey)
	return CertSample{
		Kind:               "certificate",
		NotBefore:          leaf.NotBefore,
		NotAfter:           leaf.NotAfter,
		SignatureAlgorithm: leaf.SignatureAlgorithm.String(),
		PublicKeyAlgorithm: leaf.PublicKeyAlgorithm.String(),
		KeyBits:            bits,
		Issuer:             leaf.Issuer.String(),
		Subject:            leaf.Subject.String(),
		SerialNumber:       serial,
		DNSNames:           leaf.DNSNames,
		Fingerprint:        hex.EncodeToString(sum[:]),
	}
}

// verifyCertChain validates leaf against the system roots, using peers as the
// candidate intermediates, and checks it covers serverName. It returns the
// verification error string, or "" when the certificate is valid.
func verifyCertChain(leaf *x509.Certificate, peers []*x509.Certificate, serverName string) string {
	roots, _ := x509.SystemCertPool()
	inter := x509.NewCertPool()
	for _, c := range peers {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: serverName, Roots: roots, Intermediates: inter}); err != nil {
		return err.Error()
	}
	return ""
}

// defaultCertSampler dials host:port over TLS (without failing on an invalid
// certificate, so it can be inspected) and reads the leaf certificate, optionally
// verifying the chain and hostname against the system roots.
func defaultCertSampler(ctx context.Context, host, port, serverName string, verify bool) (CertSample, error) {
	cfg := &tls.Config{InsecureSkipVerify: true, ServerName: serverName} //nolint:gosec // inspected manually below
	conn, err := (&tls.Dialer{Config: cfg}).DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return CertSample{}, err
	}
	defer conn.Close()
	state := conn.(*tls.Conn).ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return CertSample{}, errors.New("no certificate presented")
	}
	leaf := state.PeerCertificates[0]

	s := certSampleFromCert(leaf)
	if verify {
		s.VerifyError = verifyCertChain(leaf, state.PeerCertificates[1:], serverName)
	}
	return s, nil
}
