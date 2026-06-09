package checks

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// CertSample is one observation of a TLS endpoint's leaf certificate.
type CertSample struct {
	NotBefore          time.Time
	NotAfter           time.Time
	SignatureAlgorithm string
	PublicKeyAlgorithm string
	Issuer             string
	Subject            string
	SerialNumber       string   // hex
	DNSNames           []string // subject alternative names
	Fingerprint        string   // SHA-256 of the DER, hex
	VerifyError        string   // chain/hostname/validity error when verify requested, else ""
}

// CertSamplerFunc fetches a TLS endpoint's leaf certificate. Injected for tests;
// the default dials the host and inspects the presented chain.
type CertSamplerFunc func(ctx context.Context, host, port, serverName string, verify bool) (CertSample, error)

// certCheck inspects a TLS certificate. It is condition-style (OK==true means an
// alert): the certificate is expiring within expiresInDays, already expired or
// not yet valid, fails chain/hostname verification (verify), or — between cycles —
// its signature algorithm, issuer or fingerprint changed. It is stateful for the
// change detection (a pointer), so change conditions work when built once (a host
// watch); as a per-service check only the level conditions apply.
type certCheck struct {
	base
	host           string
	port           string
	serverName     string
	expiresInDays  int
	onAlgoChange   bool
	onIssuerChange bool
	onChange       bool
	verify         bool
	sampler        CertSamplerFunc

	primed     bool
	lastAlgo   string
	lastIssuer string
	lastFP     string
}

func (c *certCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	sampler := c.sampler
	if sampler == nil {
		sampler = defaultCertSampler
	}
	s, err := sampler(ctx, c.host, c.port, c.serverName, c.verify)
	if err != nil {
		// Cannot retrieve the certificate (network/TLS error): not an alert here —
		// use a tcp/http check for reachability.
		return c.result(false, fmt.Sprintf("cert %s:%s: %v", c.host, c.port, err), start)
	}

	now := time.Now()
	daysLeft := int(s.NotAfter.Sub(now).Hours() / 24)
	var problems []string

	switch {
	case now.After(s.NotAfter):
		problems = append(problems, "expired")
	case now.Before(s.NotBefore):
		problems = append(problems, "not yet valid")
	case c.expiresInDays > 0 && daysLeft < c.expiresInDays:
		problems = append(problems, fmt.Sprintf("expires in %d days", daysLeft))
	}
	if c.verify && s.VerifyError != "" {
		problems = append(problems, "chain: "+s.VerifyError)
	}

	if !c.primed {
		c.primed = true
	} else {
		if c.onAlgoChange && s.SignatureAlgorithm != c.lastAlgo {
			problems = append(problems, "signature algorithm "+c.lastAlgo+" -> "+s.SignatureAlgorithm)
		}
		if c.onIssuerChange && s.Issuer != c.lastIssuer {
			problems = append(problems, "issuer changed")
		}
		if c.onChange && s.Fingerprint != c.lastFP {
			problems = append(problems, "certificate changed")
		}
	}
	c.lastAlgo, c.lastIssuer, c.lastFP = s.SignatureAlgorithm, s.Issuer, s.Fingerprint

	ok := len(problems) > 0
	msg := fmt.Sprintf("%s: valid %d days (until %s), %s, issuer %s", c.host, daysLeft, s.NotAfter.Format("2006-01-02"), s.SignatureAlgorithm, s.Issuer)
	if ok {
		msg = c.host + ": " + strings.Join(problems, "; ")
	}
	res := c.result(ok, msg, start)
	res.Data = map[string]any{
		"host":                 c.host,
		"days_left":            daysLeft,
		"value":                daysLeft,
		"not_before":           s.NotBefore.Format(time.RFC3339),
		"not_after":            s.NotAfter.Format(time.RFC3339),
		"issuer":               s.Issuer,
		"subject":              s.Subject,
		"serial_number":        s.SerialNumber,
		"dns_names":            s.DNSNames,
		"signature_algorithm":  s.SignatureAlgorithm,
		"public_key_algorithm": s.PublicKeyAlgorithm,
		"fingerprint":          s.Fingerprint,
	}
	return res
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

	sum := sha256.Sum256(leaf.Raw)
	var serial string
	if leaf.SerialNumber != nil {
		serial = leaf.SerialNumber.Text(16)
	}
	s := CertSample{
		NotBefore:          leaf.NotBefore,
		NotAfter:           leaf.NotAfter,
		SignatureAlgorithm: leaf.SignatureAlgorithm.String(),
		PublicKeyAlgorithm: leaf.PublicKeyAlgorithm.String(),
		Issuer:             leaf.Issuer.String(),
		Subject:            leaf.Subject.String(),
		SerialNumber:       serial,
		DNSNames:           leaf.DNSNames,
		Fingerprint:        hex.EncodeToString(sum[:]),
	}
	if verify {
		roots, _ := x509.SystemCertPool()
		inter := x509.NewCertPool()
		for _, c := range state.PeerCertificates[1:] {
			inter.AddCert(c)
		}
		if _, verr := leaf.Verify(x509.VerifyOptions{DNSName: serverName, Roots: roots, Intermediates: inter}); verr != nil {
			s.VerifyError = verr.Error()
		}
	}
	return s, nil
}
