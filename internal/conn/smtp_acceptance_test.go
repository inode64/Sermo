package conn

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

const smtpAcceptanceTestTimeout = 2 * time.Second

func TestSMTPAcceptanceUsesSTARTTLSAndNeverSendsData(t *testing.T) {
	serverTLS, clientTLS := smtpAcceptanceTestTLS(t)
	port, commands, done := serveSMTPAcceptance(t, serverTLS, "250 2.1.5 recipient ok")
	proto := smtpAcceptanceTestProtocol(clientTLS)

	ctx, cancel := context.WithTimeout(context.Background(), smtpAcceptanceTestTimeout)
	defer cancel()
	res, err := proto.Probe(ctx, smtpAcceptanceTestConfig(port))
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.Failure != "" {
		t.Fatalf("Probe failure = %q", res.Failure)
	}
	if res.Extra[ExtraKeySMTPAccepted] != "true" ||
		res.Extra[ExtraKeySMTPStartTLS] != "true" ||
		res.Extra[ExtraKeySMTPMXHost] != "127.0.0.1" ||
		res.Extra[ExtraKeySMTPStage] != smtpAcceptanceStageRCPTTo {
		t.Fatalf("result = %+v", res)
	}

	got := waitSMTPAcceptanceCommands(t, commands, done)
	for _, prefix := range []string{"EHLO mail.sender.example", "STARTTLS", "MAIL FROM:<probe@sender.example>", "RCPT TO:<canary@recipient.example>", "RSET", "QUIT"} {
		if !smtpCommandsContain(got, prefix) {
			t.Errorf("commands missing %q: %v", prefix, got)
		}
	}
	if smtpCommandsContain(got, "DATA") {
		t.Fatalf("acceptance probe sent DATA: %v", got)
	}
}

func TestSMTPAcceptancePreservesRemoteRejection(t *testing.T) {
	port, commands, done := serveSMTPAcceptance(t, nil, "550 5.7.511 sending IP blocked")
	proto := smtpAcceptanceTestProtocol(nil)
	cfg := smtpAcceptanceTestConfig(port)
	cfg.Params[ParamKeySMTPStartTLS] = SMTPStartTLSOpportunistic

	res, err := proto.Probe(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Probe returned transport error for SMTP rejection: %v", err)
	}
	if res.Failure == "" || !strings.Contains(res.Failure, "550") {
		t.Fatalf("failure = %q", res.Failure)
	}
	want := map[string]string{
		ExtraKeySMTPAccepted:       "false",
		ExtraKeySMTPEnhancedStatus: "5.7.511",
		ExtraKeySMTPStage:          smtpAcceptanceStageRCPTTo,
		ExtraKeySMTPStatus:         smtpAcceptanceStatusPermanent,
		ExtraKeySMTPStatusCode:     "550",
	}
	for key, value := range want {
		if res.Extra[key] != value {
			t.Errorf("%s = %q, want %q (all=%v)", key, res.Extra[key], value, res.Extra)
		}
	}
	got := waitSMTPAcceptanceCommands(t, commands, done)
	if smtpCommandsContain(got, "DATA") {
		t.Fatalf("rejected probe sent DATA: %v", got)
	}
}

func TestSMTPAcceptanceClassifiesTemporaryRejection(t *testing.T) {
	port, commands, done := serveSMTPAcceptance(t, nil, "451 4.7.0 reputation throttled")
	cfg := smtpAcceptanceTestConfig(port)
	cfg.Params[ParamKeySMTPStartTLS] = SMTPStartTLSOpportunistic
	res, err := smtpAcceptanceTestProtocol(nil).Probe(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.Extra[ExtraKeySMTPStatus] != smtpAcceptanceStatusTemporary ||
		res.Extra[ExtraKeySMTPEnhancedStatus] != "4.7.0" ||
		res.Extra[ExtraKeySMTPStatusCode] != "451" {
		t.Fatalf("result = %+v", res)
	}
	_ = waitSMTPAcceptanceCommands(t, commands, done)
}

func TestSMTPAcceptanceRequiresAdvertisedSTARTTLS(t *testing.T) {
	port, commands, done := serveSMTPAcceptance(t, nil, "250 recipient ok")
	res, err := smtpAcceptanceTestProtocol(nil).Probe(context.Background(), smtpAcceptanceTestConfig(port))
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.Extra[ExtraKeySMTPStatus] != smtpAcceptanceStatusPolicy ||
		res.Extra[ExtraKeySMTPStage] != smtpAcceptanceStageStartTLS ||
		!strings.Contains(res.Failure, "STARTTLS") {
		t.Fatalf("result = %+v", res)
	}
	got := waitSMTPAcceptanceCommands(t, commands, done)
	for _, forbidden := range []string{"MAIL FROM", "RCPT TO", "DATA"} {
		if smtpCommandsContain(got, forbidden) {
			t.Fatalf("required STARTTLS failure sent %s: %v", forbidden, got)
		}
	}
}

func TestSMTPAcceptanceFallsBackToNextMXAfterDialError(t *testing.T) {
	port, commands, done := serveSMTPAcceptance(t, nil, "250 recipient ok")
	proto := smtpAcceptanceProtocol{lookupMX: func(context.Context, string, string) ([]*net.MX, error) {
		return []*net.MX{{Host: "127.0.0.2.", Pref: 10}, {Host: "127.0.0.1.", Pref: 20}}, nil
	}}
	cfg := smtpAcceptanceTestConfig(port)
	cfg.Params[ParamKeySMTPStartTLS] = SMTPStartTLSOpportunistic
	res, err := proto.Probe(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.Extra[ExtraKeySMTPMXHost] != "127.0.0.1" || res.Extra[ExtraKeySMTPAccepted] != "true" {
		t.Fatalf("result = %+v", res)
	}
	_ = waitSMTPAcceptanceCommands(t, commands, done)
}

func TestSMTPAcceptanceDoesNotMaskRemoteRejectionWithLaterMX(t *testing.T) {
	port, commands, done := serveSMTPAcceptance(t, nil, "451 4.7.0 reputation throttled")
	proto := smtpAcceptanceProtocol{lookupMX: func(context.Context, string, string) ([]*net.MX, error) {
		return []*net.MX{{Host: "127.0.0.1.", Pref: 10}, {Host: "127.0.0.2.", Pref: 20}}, nil
	}}
	cfg := smtpAcceptanceTestConfig(port)
	cfg.Params[ParamKeySMTPStartTLS] = SMTPStartTLSOpportunistic

	res, err := proto.Probe(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Probe returned transport error after an authoritative SMTP rejection: %v", err)
	}
	if res.Extra[ExtraKeySMTPMXHost] != "127.0.0.1" ||
		res.Extra[ExtraKeySMTPStatus] != smtpAcceptanceStatusTemporary {
		t.Fatalf("result = %+v", res)
	}
	_ = waitSMTPAcceptanceCommands(t, commands, done)
}

func TestSMTPAcceptanceReportsNullMXAsPolicyFailure(t *testing.T) {
	proto := smtpAcceptanceProtocol{lookupMX: func(context.Context, string, string) ([]*net.MX, error) {
		return []*net.MX{{Host: ".", Pref: 0}}, nil
	}}

	res, err := proto.Probe(context.Background(), smtpAcceptanceTestConfig(25))
	if err != nil {
		t.Fatalf("Probe returned transport error for null MX: %v", err)
	}
	if res.Failure == "" ||
		res.Extra[ExtraKeySMTPMXHost] != "." ||
		res.Extra[ExtraKeySMTPStage] != smtpAcceptanceStageMX ||
		res.Extra[ExtraKeySMTPStatus] != smtpAcceptanceStatusPolicy {
		t.Fatalf("result = %+v", res)
	}
}

func TestSMTPAcceptanceClassifiesUnexpectedReplyAsProtocolFailure(t *testing.T) {
	res, err := smtpAcceptanceStepResult(Result{Extra: map[string]string{}}, smtpAcceptanceStageRCPTTo, &textproto.Error{
		Code: 354,
		Msg:  "unexpected intermediate reply",
	})
	if err != nil {
		t.Fatalf("smtpAcceptanceStepResult: %v", err)
	}
	if res.Extra[ExtraKeySMTPStatus] != smtpAcceptanceStatusProtocol {
		t.Fatalf("smtp_status = %q, want %q", res.Extra[ExtraKeySMTPStatus], smtpAcceptanceStatusProtocol)
	}
}

func TestSMTPAcceptanceHonorsContextDeadline(t *testing.T) {
	port := serveOnce(t, func(net.Conn) {
		time.Sleep(smtpAcceptanceTestTimeout)
	})
	proto := smtpAcceptanceTestProtocol(nil)
	cfg := smtpAcceptanceTestConfig(port)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := proto.Probe(ctx, cfg); err == nil {
		t.Fatal("Probe error = nil, want deadline failure")
	}
}

func TestSMTPAcceptancePassesInterfaceToMXLookup(t *testing.T) {
	var gotInterface string
	proto := smtpAcceptanceProtocol{lookupMX: func(_ context.Context, _ string, iface string) ([]*net.MX, error) {
		gotInterface = iface
		return nil, errors.New("lookup stopped for test")
	}}
	cfg := smtpAcceptanceTestConfig(25)
	cfg.Interface = "eth-test"
	_, _ = proto.Probe(context.Background(), cfg)
	if gotInterface != "eth-test" {
		t.Fatalf("lookup interface = %q, want eth-test", gotInterface)
	}
}

func TestParseSMTPMailboxAndPolicy(t *testing.T) {
	address, domain, err := ParseSMTPMailbox("Probe@Mail.Example")
	if err != nil || address != "Probe@Mail.Example" || domain != "mail.example" {
		t.Fatalf("ParseSMTPMailbox = %q, %q, %v", address, domain, err)
	}
	for _, value := range []string{"", "Name <a@example.com>", "a@localhost", "a@example.com\r\nRCPT TO:<x@y.example>", "@example.com"} {
		if _, _, err := ParseSMTPMailbox(value); err == nil {
			t.Errorf("ParseSMTPMailbox(%q) error = nil", value)
		}
	}
	if !ValidSMTPHelo("mail.sender.example") || ValidSMTPHelo("localhost") || ValidSMTPHelo("bad name.example") {
		t.Fatal("ValidSMTPHelo accepted an invalid name or rejected a valid FQDN")
	}
	if NormalizeSMTPStartTLS("") != SMTPStartTLSRequired || ValidSMTPStartTLS("disabled") {
		t.Fatal("STARTTLS policy defaults or validation changed")
	}
	if got := truncateSMTPReply(strings.Repeat("x", smtpAcceptanceReplyMaxBytes+100)); len(got) != smtpAcceptanceReplyMaxBytes {
		t.Fatalf("bounded reply length = %d", len(got))
	}
}

func TestParseSMTPAcceptanceEnvelope(t *testing.T) {
	envelope, err := ParseSMTPAcceptanceEnvelope(
		"mail.sender.example",
		"Probe@Sender.Example",
		"Canary@Recipient.Example",
		"",
	)
	if err != nil {
		t.Fatalf("ParseSMTPAcceptanceEnvelope: %v", err)
	}
	if envelope.Helo != "mail.sender.example" ||
		envelope.MailFrom != "Probe@Sender.Example" ||
		envelope.Recipient != "Canary@Recipient.Example" ||
		envelope.RecipientDomain != "recipient.example" ||
		envelope.StartTLS != SMTPStartTLSRequired {
		t.Fatalf("envelope = %+v", envelope)
	}

	for _, test := range []struct {
		name      string
		helo      string
		mailFrom  string
		recipient string
		startTLS  string
		want      string
	}{
		{name: "helo", helo: "localhost", mailFrom: "a@sender.example", recipient: "b@recipient.example", want: "helo"},
		{name: "mail from", helo: "mail.sender.example", mailFrom: "invalid", recipient: "b@recipient.example", want: "mail_from"},
		{name: "recipient", helo: "mail.sender.example", mailFrom: "a@sender.example", recipient: "invalid", want: "recipient"},
		{name: "starttls", helo: "mail.sender.example", mailFrom: "a@sender.example", recipient: "b@recipient.example", startTLS: "disabled", want: "starttls"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, parseErr := ParseSMTPAcceptanceEnvelope(test.helo, test.mailFrom, test.recipient, test.startTLS)
			if parseErr == nil || !strings.Contains(parseErr.Error(), test.want) {
				t.Fatalf("error = %v, want field %q", parseErr, test.want)
			}
		})
	}
}

func smtpAcceptanceTestConfig(port int) Config {
	return Config{Port: port, Params: map[string]string{
		ParamKeySMTPHelo:      "mail.sender.example",
		ParamKeySMTPMailFrom:  "probe@sender.example",
		ParamKeySMTPRecipient: "canary@recipient.example",
		ParamKeySMTPStartTLS:  SMTPStartTLSRequired,
	}}
}

func smtpAcceptanceTestProtocol(clientTLS *tls.Config) smtpAcceptanceProtocol {
	proto := smtpAcceptanceProtocol{lookupMX: func(context.Context, string, string) ([]*net.MX, error) {
		return []*net.MX{{Host: "127.0.0.1.", Pref: 10}}, nil
	}}
	if clientTLS != nil {
		proto.tlsConfig = func(string) *tls.Config { return clientTLS.Clone() }
	}
	return proto
}

func serveSMTPAcceptance(t *testing.T, serverTLS *tls.Config, rcptReply string) (int, <-chan string, <-chan struct{}) {
	t.Helper()
	commands := make(chan string, 16)
	done := make(chan struct{})
	port := serveOnce(t, func(conn net.Conn) {
		defer close(done)
		writeSMTPTestReply(conn, "220 mx test ready")
		reader := bufio.NewReader(conn)
		tlsActive := false
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			command := strings.TrimRight(line, "\r\n")
			commands <- command
			upper := strings.ToUpper(command)
			switch {
			case strings.HasPrefix(upper, "EHLO "), strings.HasPrefix(upper, "HELO "):
				if serverTLS != nil && !tlsActive {
					_, _ = fmt.Fprint(conn, "250-localhost\r\n250 STARTTLS\r\n")
				} else {
					writeSMTPTestReply(conn, "250 localhost")
				}
			case upper == "STARTTLS":
				writeSMTPTestReply(conn, "220 2.0.0 begin TLS")
				tlsConn := tls.Server(conn, serverTLS)
				if tlsConn.Handshake() != nil {
					return
				}
				conn, reader, tlsActive = tlsConn, bufio.NewReader(tlsConn), true
			case strings.HasPrefix(upper, "MAIL FROM:"):
				writeSMTPTestReply(conn, "250 2.1.0 sender ok")
			case strings.HasPrefix(upper, "RCPT TO:"):
				writeSMTPTestReply(conn, rcptReply)
				if strings.HasPrefix(rcptReply, "4") || strings.HasPrefix(rcptReply, "5") {
					return
				}
			case upper == "RSET":
				writeSMTPTestReply(conn, "250 2.0.0 reset")
			case upper == "QUIT":
				writeSMTPTestReply(conn, "221 2.0.0 bye")
				return
			case upper == "DATA":
				writeSMTPTestReply(conn, "554 DATA forbidden in acceptance test")
				return
			default:
				writeSMTPTestReply(conn, "500 unexpected command")
				return
			}
		}
	})
	return port, commands, done
}

func writeSMTPTestReply(conn net.Conn, reply string) {
	_, _ = fmt.Fprint(conn, reply+"\r\n")
}

func waitSMTPAcceptanceCommands(t *testing.T, commands <-chan string, done <-chan struct{}) []string {
	t.Helper()
	select {
	case <-done:
	case <-time.After(smtpAcceptanceTestTimeout):
		t.Fatal("SMTP test server did not finish")
	}
	var out []string
	for {
		select {
		case command := <-commands:
			out = append(out, command)
		default:
			return out
		}
	}
}

func smtpCommandsContain(commands []string, prefix string) bool {
	prefix = strings.ToUpper(prefix)
	for _, command := range commands {
		if strings.HasPrefix(strings.ToUpper(command), prefix) {
			return true
		}
	}
	return false
}

func smtpAcceptanceTestTLS(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		&tls.Config{ServerName: "127.0.0.1", RootCAs: roots, MinVersion: tls.VersionTLS12}
}
