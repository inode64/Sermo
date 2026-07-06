package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	gomail "github.com/wneessen/go-mail"
	gomailsmtp "github.com/wneessen/go-mail/smtp"

	"sermo/internal/cfgval"
)

// dialTimeout bounds the SMTP connection attempt, and is the fallback deadline
// for the whole SMTP conversation when the caller's context carries none, so a
// dead or stalled mail server cannot hang a watch cycle.
const dialTimeout = 15 * time.Second

const minSMTPTimeout = time.Nanosecond

const (
	schemeSMTP       = "smtp"
	schemeSMTPS      = "smtps"
	smtpDefaultPort  = "587"
	smtpsDefaultPort = "465"
)

// emailDSN is a parsed SMTP DSN: smtp://[user:pass@]host[:port] (STARTTLS) or
// smtps://… (implicit TLS, default port 465).
type emailDSN struct {
	host        string
	port        string
	user        string
	pass        string
	implicitTLS bool
}

func (d emailDSN) addr() string { return net.JoinHostPort(d.host, d.port) }

// emailSender delivers a built message; injected so tests do not hit the network.
type emailSender func(ctx context.Context, dsn emailDSN, from string, to []string, msg Message) error

// Email is an SMTP notifier. Configured with a DSN, a from address and one or
// more recipients.
type Email struct {
	name string
	from string
	to   []string
	dsn  emailDSN
	send emailSender
}

// Name returns the notifier's configured name.
func (e *Email) Name() string { return e.name }

// Type returns the notifier type identifier.
func (e *Email) Type() string { return notifierTypeEmail }

// Send delivers the message over SMTP.
func (e *Email) Send(ctx context.Context, msg Message) error {
	send := e.send
	if send == nil {
		send = smtpSend
	}
	return send(ctx, e.dsn, e.from, e.to, msg)
}

// buildEmail constructs an Email notifier from a config entry.
func buildEmail(name string, entry map[string]any) (Notifier, error) {
	dsnStr, _ := entry[keyDSN].(string)
	if dsnStr == "" {
		return nil, errors.New("email notifier requires a dsn")
	}
	dsn, err := parseEmailDSN(dsnStr)
	if err != nil {
		return nil, err
	}
	from, _ := entry[keyFrom].(string)
	if from == "" {
		return nil, errors.New("email notifier requires a from address")
	}
	to := cfgval.StringList(entry[keyTo])
	if len(to) == 0 {
		return nil, errors.New("email notifier requires at least one to address")
	}
	return &Email{name: name, from: from, to: to, dsn: dsn, send: smtpSend}, nil
}

// parseEmailDSN parses smtp:// or smtps:// DSNs.
func parseEmailDSN(s string) (emailDSN, error) {
	u, err := url.Parse(s)
	if err != nil {
		return emailDSN{}, fmt.Errorf("invalid dsn: %w", err)
	}
	var d emailDSN
	switch u.Scheme {
	case schemeSMTP:
		d.implicitTLS = false
	case schemeSMTPS:
		d.implicitTLS = true
	default:
		return emailDSN{}, fmt.Errorf("dsn scheme %q must be %s or %s", u.Scheme, schemeSMTP, schemeSMTPS)
	}
	d.host = u.Hostname()
	if d.host == "" {
		return emailDSN{}, errors.New("dsn requires a host")
	}
	d.port = u.Port()
	if d.port == "" {
		d.port = d.defaultPort()
	}
	if u.User != nil {
		d.user = u.User.Username()
		d.pass, _ = u.User.Password()
	}
	return d, nil
}

func (d emailDSN) defaultPort() string {
	if d.implicitTLS {
		return smtpsDefaultPort
	}
	return smtpDefaultPort
}

// smtpSend connects per the DSN (implicit TLS or opportunistic STARTTLS),
// authenticates when credentials are present (refusing PLAIN over cleartext),
// and delivers the message.
func smtpSend(ctx context.Context, d emailDSN, from string, to []string, msg Message) error {
	return smtpSendWithTLSConfig(ctx, d, from, to, msg, smtpTLSConfig(d.host))
}

func smtpSendWithTLSConfig(ctx context.Context, d emailDSN, from string, to []string, msg Message, tlsCfg *tls.Config) error {
	m, err := buildMailMessage(from, to, msg)
	if err != nil {
		return err
	}
	client, err := newSMTPClient(d, tlsCfg, smtpTimeout(ctx))
	if err != nil {
		return err
	}
	if err := client.DialAndSendWithContext(ctx, m); err != nil {
		return fmt.Errorf("send email via %s: %w", d.addr(), err)
	}
	return nil
}

func smtpTimeout(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return minSMTPTimeout
		}
		return remaining
	}
	return dialTimeout
}

func smtpTLSConfig(host string) *tls.Config {
	return &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
}

func newSMTPClient(d emailDSN, tlsCfg *tls.Config, timeout time.Duration) (*gomail.Client, error) {
	port, err := strconv.Atoi(d.port)
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid SMTP port %q", d.port)
	}

	opts := []gomail.Option{
		gomail.WithPort(port),
		gomail.WithTimeout(timeout),
		gomail.WithTLSConfig(tlsCfg),
		gomail.WithoutNoop(),
	}
	if !d.implicitTLS {
		opts = append(opts, gomail.WithDialContextFunc(smtpDialContext(timeout)))
	}

	switch {
	case d.implicitTLS:
		opts = append(opts, gomail.WithSSL())
	case d.user != "":
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSMandatory))
	default:
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSOpportunistic))
	}

	if d.user != "" {
		auth := gomailsmtp.PlainAuth("", d.user, d.pass, d.host, false)
		opts = append(opts, gomail.WithSMTPAuthCustom(auth))
	}

	client, err := gomail.NewClient(d.host, opts...)
	if err != nil {
		return nil, fmt.Errorf("build SMTP client: %w", err)
	}
	return client, nil
}

func smtpDialContext(timeout time.Duration) gomail.DialContextFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		dialer := &net.Dialer{Timeout: timeout}
		conn, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		if deadline, ok := ctx.Deadline(); ok {
			conn = &boundedDeadlineConn{Conn: conn, limit: deadline}
		}
		return conn, nil
	}
}

type boundedDeadlineConn struct {
	net.Conn
	limit time.Time
}

func (c *boundedDeadlineConn) SetDeadline(t time.Time) error {
	return c.Conn.SetDeadline(c.deadline(t))
}

func (c *boundedDeadlineConn) SetReadDeadline(t time.Time) error {
	return c.Conn.SetReadDeadline(c.deadline(t))
}

func (c *boundedDeadlineConn) SetWriteDeadline(t time.Time) error {
	return c.Conn.SetWriteDeadline(c.deadline(t))
}

func (c *boundedDeadlineConn) deadline(t time.Time) time.Time {
	if c.limit.IsZero() {
		return t
	}
	if t.IsZero() || t.After(c.limit) {
		return c.limit
	}
	return t
}

func buildMailMessage(from string, to []string, msg Message) (*gomail.Msg, error) {
	m := gomail.NewMsg(gomail.WithCharset(gomail.CharsetUTF8), gomail.WithEncoding(gomail.NoEncoding))
	if err := m.From(from); err != nil {
		return nil, fmt.Errorf("from address: %w", err)
	}
	if err := m.To(to...); err != nil {
		return nil, fmt.Errorf("to address: %w", err)
	}
	m.Subject(sanitizeHeader(msg.Subject))
	m.SetDate()
	m.SetBodyString(gomail.TypeTextPlain, crlfBody(msg.Body))
	if msg.HTML != "" {
		m.AddAlternativeString(gomail.TypeTextHTML, crlfBody(msg.HTML))
	}
	return m, nil
}

func crlfBody(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	body = strings.ReplaceAll(body, "\n", "\r\n")
	if !strings.HasSuffix(body, "\r\n") {
		body += "\r\n"
	}
	return body
}

// sanitizeHeader strips CR/LF to prevent header injection from a check message.
func sanitizeHeader(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}
