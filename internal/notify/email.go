package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
	"strings"
	"time"
)

// dialTimeout bounds the SMTP connection attempt so a dead mail server cannot
// stall a watch cycle.
const dialTimeout = 15 * time.Second

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

func (e *Email) Name() string { return e.name }
func (e *Email) Type() string { return "email" }

func (e *Email) Send(ctx context.Context, msg Message) error {
	send := e.send
	if send == nil {
		send = smtpSend
	}
	return send(ctx, e.dsn, e.from, e.to, msg)
}

// buildEmail constructs an Email notifier from a config entry.
func buildEmail(name string, entry map[string]any) (Notifier, error) {
	dsnStr, _ := entry["dsn"].(string)
	if dsnStr == "" {
		return nil, errors.New("email notifier requires a dsn")
	}
	dsn, err := parseEmailDSN(dsnStr)
	if err != nil {
		return nil, err
	}
	from, _ := entry["from"].(string)
	if from == "" {
		return nil, errors.New("email notifier requires a from address")
	}
	to := stringList(entry["to"])
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
	case "smtp":
		d.implicitTLS = false
	case "smtps":
		d.implicitTLS = true
	default:
		return emailDSN{}, fmt.Errorf("dsn scheme %q must be smtp or smtps", u.Scheme)
	}
	d.host = u.Hostname()
	if d.host == "" {
		return emailDSN{}, errors.New("dsn requires a host")
	}
	d.port = u.Port()
	if d.port == "" {
		if d.implicitTLS {
			d.port = "465"
		} else {
			d.port = "587"
		}
	}
	if u.User != nil {
		d.user = u.User.Username()
		d.pass, _ = u.User.Password()
	}
	return d, nil
}

// smtpSend connects per the DSN (implicit TLS or opportunistic STARTTLS),
// authenticates when credentials are present (refusing PLAIN over cleartext),
// and delivers the message. Uses only net/smtp (no external dependency).
func smtpSend(ctx context.Context, d emailDSN, from string, to []string, msg Message) error {
	raw := buildMessage(from, to, msg)
	tlsCfg := &tls.Config{ServerName: d.host}
	dialer := &net.Dialer{Timeout: dialTimeout}

	var conn net.Conn
	var err error
	if d.implicitTLS {
		conn, err = tls.DialWithDialer(dialer, "tcp", d.addr(), tlsCfg)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", d.addr())
	}
	if err != nil {
		return fmt.Errorf("dial %s: %w", d.addr(), err)
	}

	c, err := smtp.NewClient(conn, d.host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()

	tlsActive := d.implicitTLS
	if !d.implicitTLS {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("starttls: %w", err)
			}
			tlsActive = true
		}
	}
	if d.user != "" {
		if !tlsActive {
			return errors.New("refusing to send SMTP credentials over an unencrypted connection")
		}
		if err := c.Auth(smtp.PlainAuth("", d.user, d.pass, d.host)); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}

	if err := c.Mail(bareAddr(from)); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	for _, rcpt := range to {
		if err := c.Rcpt(bareAddr(rcpt)); err != nil {
			return fmt.Errorf("RCPT TO %s: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// buildMessage renders a minimal RFC 5322 plain-text message.
func buildMessage(from string, to []string, msg Message) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	b.WriteString("Subject: " + sanitizeHeader(msg.Subject) + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(msg.Body, "\n", "\r\n"))
	if !strings.HasSuffix(msg.Body, "\n") {
		b.WriteString("\r\n")
	}
	return []byte(b.String())
}

// bareAddr extracts the address from a "Name <addr>" string, or returns it as-is.
func bareAddr(s string) string {
	if a, err := mail.ParseAddress(s); err == nil {
		return a.Address
	}
	return s
}

// sanitizeHeader strips CR/LF to prevent header injection from a check message.
func sanitizeHeader(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}
