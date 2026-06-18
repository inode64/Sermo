package notify

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

// TestSMTPSendHonorsContextDeadline proves the SMTP conversation is bounded: a
// server that accepts the connection then never sends its greeting must not hang
// smtpSend — the connection deadline (derived from ctx) returns an error.
func TestSMTPSendHonorsContextDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	// Accept and hold the connection open without ever replying.
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		<-make(chan struct{})
	}()

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	dsn := emailDSN{host: host, port: port}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- smtpSend(ctx, dsn, "from@example.com", []string{"to@example.com"}, Message{Subject: "s", Body: "b"})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("smtpSend returned nil; want a deadline error from the stalled server")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("smtpSend hung past the context deadline")
	}
}

func TestSMTPSendRequiresSTARTTLSForCredentials(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	commands := make(chan string, 8)
	go servePlainSMTP(t, ln, commands)

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	dsn := emailDSN{host: host, port: port, user: "ops", pass: "secret"}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err = smtpSend(ctx, dsn, "from@example.com", []string{"to@example.com"}, Message{Subject: "s", Body: "b"})
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("smtpSend err = %v, want STARTTLS refusal", err)
	}
	for {
		select {
		case cmd := <-commands:
			if strings.HasPrefix(strings.ToUpper(cmd), "AUTH") {
				t.Fatalf("AUTH was sent before STARTTLS: %q", cmd)
			}
		default:
			return
		}
	}
}

func TestSMTPSendImplicitTLSAuthenticates(t *testing.T) {
	serverTLS, clientTLS := testSMTPServerTLS(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverTLS)
	if err != nil {
		t.Fatalf("listen TLS: %v", err)
	}
	defer ln.Close()

	commands := make(chan string, 16)
	go servePlainSMTP(t, ln, commands)

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	dsn := emailDSN{host: host, port: port, user: "ops", pass: "secret", implicitTLS: true}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := smtpSendWithTLSConfig(ctx, dsn, "from@example.com", []string{"to@example.com"}, Message{Subject: "s", Body: "b"}, clientTLS); err != nil {
		t.Fatalf("smtpSendWithTLSConfig: %v", err)
	}
	if !hasSMTPCommand(commands, "AUTH PLAIN") {
		t.Fatal("SMTPS send did not authenticate")
	}
}

func TestSMTPTimeoutUsesCallerDeadlineOrFallback(t *testing.T) {
	if got := smtpTimeout(context.Background()); got != dialTimeout {
		t.Fatalf("smtpTimeout without deadline = %v, want %v", got, dialTimeout)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	got := smtpTimeout(ctx)
	if got <= 59*time.Minute || got > time.Hour {
		t.Fatalf("smtpTimeout with caller deadline = %v, want close to 1h", got)
	}

	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	if got := smtpTimeout(expired); got != minSMTPTimeout {
		t.Fatalf("smtpTimeout with expired context = %v, want %v", got, minSMTPTimeout)
	}
}

func servePlainSMTP(t *testing.T, ln net.Listener, commands chan<- string) {
	t.Helper()
	c, err := ln.Accept()
	if err != nil {
		return
	}
	defer c.Close()
	br := bufio.NewReader(c)
	write := func(format string, args ...any) {
		_, _ = fmt.Fprintf(c, format+"\r\n", args...)
	}
	write("220 local test smtp")
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimRight(line, "\r\n")
		commands <- cmd
		upper := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			write("250-localhost")
			write("250-8BITMIME")
			write("250 AUTH PLAIN")
		case strings.HasPrefix(upper, "AUTH PLAIN"):
			write("235 2.7.0 authentication successful")
		case strings.HasPrefix(upper, "DATA"):
			write("354 end data with <CR><LF>.<CR><LF>")
			for {
				data, err := br.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(data, "\r\n") == "." {
					break
				}
			}
			write("250 2.0.0 queued")
		case strings.HasPrefix(upper, "QUIT"):
			write("221 bye")
			return
		default:
			write("250 ok")
		}
	}
}

func hasSMTPCommand(commands <-chan string, prefix string) bool {
	for {
		select {
		case cmd := <-commands:
			if strings.HasPrefix(strings.ToUpper(cmd), prefix) {
				return true
			}
		default:
			return false
		}
	}
}

func testSMTPServerTLS(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		&tls.Config{ServerName: "127.0.0.1", RootCAs: roots, MinVersion: tls.VersionTLS12}
}

func TestParseEmailDSN(t *testing.T) {
	cases := []struct {
		dsn         string
		host, port  string
		user, pass  string
		implicitTLS bool
		wantErr     bool
	}{
		{dsn: "smtp://smtp.example.com", host: "smtp.example.com", port: "587"},
		{dsn: "smtps://smtp.example.com", host: "smtp.example.com", port: "465", implicitTLS: true},
		{dsn: "smtp://user:pass@mail.example.com:2525", host: "mail.example.com", port: "2525", user: "user", pass: "pass"},
		{dsn: "ftp://x", wantErr: true},
		{dsn: "smtp://", wantErr: true},
	}
	for _, tc := range cases {
		d, err := parseEmailDSN(tc.dsn)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: expected error", tc.dsn)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.dsn, err)
		}
		if d.host != tc.host || d.port != tc.port || d.user != tc.user || d.pass != tc.pass || d.implicitTLS != tc.implicitTLS {
			t.Fatalf("%s: got %+v", tc.dsn, d)
		}
	}
}

func TestBuildEmailRequiresFields(t *testing.T) {
	good, err := buildEmail("ops", map[string]any{
		"type": "email", "dsn": "smtp://localhost:25", "from": "sermo@x", "to": []any{"a@x", "b@x"},
	})
	if err != nil {
		t.Fatalf("valid email: %v", err)
	}
	if e := good.(*Email); len(e.to) != 2 || e.Type() != "email" || e.Name() != "ops" {
		t.Fatalf("unexpected email: %+v", e)
	}

	for _, entry := range []map[string]any{
		{"type": "email", "from": "x@y", "to": []any{"a@b"}},                            // no dsn
		{"type": "email", "dsn": "smtp://x", "to": []any{"a@b"}},                        // no from
		{"type": "email", "dsn": "smtp://x", "from": "x@y"},                             // no to
		{"type": "email", "dsn": "carrier-pigeon://x", "from": "x", "to": []any{"a@b"}}, // bad dsn
	} {
		if _, err := buildEmail("n", entry); err == nil {
			t.Fatalf("expected error for %v", entry)
		}
	}
}

func TestEmailSendDispatchesToSender(t *testing.T) {
	var gotFrom string
	var gotTo []string
	var gotMsg Message
	e := &Email{
		name: "ops", from: "Sermo <sermo@x>", to: []string{"a@x", "b@x"},
		dsn: emailDSN{host: "h", port: "25"},
		send: func(_ context.Context, _ emailDSN, from string, to []string, msg Message) error {
			gotFrom, gotTo, gotMsg = from, to, msg
			return nil
		},
	}
	if err := e.Send(context.Background(), Message{Subject: "s", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if gotFrom != "Sermo <sermo@x>" || len(gotTo) != 2 || gotMsg.Subject != "s" {
		t.Fatalf("sender got from=%q to=%v msg=%+v", gotFrom, gotTo, gotMsg)
	}
}

func TestBuildMailMessageHeadersAndInjectionGuard(t *testing.T) {
	raw := renderMailMessage(t, "sermo@example.com", []string{"a@example.com", "b@example.com"}, Message{
		Subject: "alert\r\nBcc: evil@example.com", // header-injection attempt
		Body:    "line1\nline2",
	})
	if !strings.Contains(raw, "From: <sermo@example.com>\r\n") ||
		!strings.Contains(raw, "To: <a@example.com>, <b@example.com>\r\n") {
		t.Fatalf("missing headers:\n%s", raw)
	}
	// The CRLF in the subject must not have spawned a new header line.
	if strings.Contains(raw, "\nBcc:") || strings.Count(raw, "Subject:") != 1 {
		t.Fatalf("subject not sanitized (header injection):\n%s", raw)
	}
	if !strings.Contains(raw, "line1\r\nline2") {
		t.Fatalf("body not CRLF-normalized:\n%s", raw)
	}
}

func TestBuildMailMessageEncodesNonASCIISubject(t *testing.T) {
	raw := renderMailMessage(t, "sermo@example.com", []string{"a@example.com"}, Message{
		Subject: "Alerta de memoria: 95% en café",
		Body:    "b",
	})
	// A UTF-8 subject must be RFC 2047 encoded, not emitted raw.
	if strings.Contains(raw, "Subject: Alerta de memoria") {
		t.Fatalf("non-ASCII subject emitted raw:\n%s", raw)
	}
	if !strings.Contains(raw, "Subject: =?UTF-8?") {
		t.Fatalf("subject not RFC 2047 encoded:\n%s", raw)
	}
	// A plain ASCII subject is still passed through readably.
	ascii := renderMailMessage(t, "sermo@example.com", []string{"a@example.com"}, Message{Subject: "plain alert", Body: "b"})
	if !strings.Contains(ascii, "Subject: plain alert\r\n") {
		t.Fatalf("ASCII subject should be unchanged:\n%s", ascii)
	}
}

func TestBuildMailMessageHTMLMultipart(t *testing.T) {
	raw := renderMailMessage(t, "sermo@example.com", []string{"ops@example.com"}, Message{
		Subject: "report",
		Body:    "plain body",
		HTML:    "<strong>html body</strong>",
	})
	if !strings.Contains(raw, "Content-Type: multipart/alternative;") {
		t.Fatalf("HTML message must be multipart/alternative:\n%s", raw)
	}
	if !strings.Contains(raw, "Content-Type: text/plain; charset=UTF-8") || !strings.Contains(raw, "plain body") {
		t.Fatalf("missing plain part:\n%s", raw)
	}
	if !strings.Contains(raw, "Content-Type: text/html; charset=UTF-8") || !strings.Contains(raw, "<strong>html body</strong>") {
		t.Fatalf("missing HTML part:\n%s", raw)
	}
}

func TestBuildMailMessageValidatesAddresses(t *testing.T) {
	if _, err := buildMailMessage("not an address", []string{"ops@example.com"}, Message{Subject: "s", Body: "b"}); err == nil {
		t.Fatal("buildMailMessage accepted an invalid from address")
	}
	if _, err := buildMailMessage("sermo@example.com", []string{"not an address"}, Message{Subject: "s", Body: "b"}); err == nil {
		t.Fatal("buildMailMessage accepted an invalid recipient")
	}
}

func renderMailMessage(t *testing.T, from string, to []string, msg Message) string {
	t.Helper()
	m, err := buildMailMessage(from, to, msg)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if _, err := m.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}
