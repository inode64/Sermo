package conn

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/textproto"
)

func init() { Register(smtpProtocol{}) }

// smtpProtocol probes an SMTP server natively (RFC 5321). With no user it is an
// anonymous connectivity check (greeting + EHLO). With a user/password it
// performs AUTH PLAIN. TLS is implicit (SMTPS) when enabled — use port 465.
type smtpProtocol struct{}

func (smtpProtocol) Name() string       { return ProtocolNameSMTP }
func (smtpProtocol) DefaultPort() int   { return defaultPortSMTP }
func (smtpProtocol) RequiresUser() bool { return false }

const (
	smtpAuthPlainDelimiter      = "\x00"
	smtpCommandAuthPlainFormat  = "AUTH PLAIN %s\r\n"
	smtpCommandEHLO             = "EHLO sermo\r\n"
	smtpCommandHELO             = "HELO sermo\r\n"
	smtpCommandQuit             = "QUIT\r\n"
	smtpStatusAuthSucceeded     = 235
	smtpStatusGreetingReady     = 220
	smtpStatusRequestedActionOK = 250
)

func (smtpProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	return probeBanner(ctx, cfg, defaultPortSMTP, smtpHandshake)
}

// smtpHandshake reads the 220 greeting, greets with EHLO (falling back to HELO),
// authenticates with AUTH PLAIN when a user is supplied, and quits. The
// multi-line RFC 959 reply format is parsed by net/textproto.
func smtpHandshake(rw io.ReadWriter, cfg Config) (Result, error) {
	tp := textproto.NewReader(bufio.NewReader(rw))
	code, greeting, err := tp.ReadResponse(0)
	if err != nil {
		return Result{}, err
	}
	res := Result{Extra: map[string]string{extraGreeting: greeting}}
	if code != smtpStatusGreetingReady {
		return Result{}, fmt.Errorf("unexpected greeting: %d %s", code, greeting)
	}

	if _, err := fmt.Fprint(rw, smtpCommandEHLO); err != nil {
		return Result{}, err
	}
	code, _, err = tp.ReadResponse(0)
	if err != nil {
		return Result{}, err
	}
	if code != smtpStatusRequestedActionOK {
		// Older servers may not support EHLO; try HELO.
		if _, err := fmt.Fprint(rw, smtpCommandHELO); err != nil {
			return Result{}, err
		}
		var text string
		if code, text, err = tp.ReadResponse(0); err != nil {
			return Result{}, err
		}
		if code != smtpStatusRequestedActionOK {
			return Result{}, fmt.Errorf("EHLO/HELO refused: %d %s", code, text)
		}
	}

	if cfg.User != "" {
		token := base64.StdEncoding.EncodeToString([]byte(smtpAuthPlainDelimiter + cfg.User + smtpAuthPlainDelimiter + cfg.Password))
		if _, err := fmt.Fprintf(rw, smtpCommandAuthPlainFormat, token); err != nil {
			return Result{}, err
		}
		code, text, err := tp.ReadResponse(0)
		if err != nil {
			return Result{}, err
		}
		if code != smtpStatusAuthSucceeded {
			return Result{}, fmt.Errorf("auth failed: %d %s", code, text)
		}
	}

	_, _ = fmt.Fprint(rw, smtpCommandQuit) // best effort
	return res, nil
}
