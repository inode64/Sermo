package conn

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"
)

func init() { Register(smtpProtocol{}) }

// smtpProtocol probes an SMTP server natively (RFC 5321). With no user it is an
// anonymous connectivity check (greeting + EHLO). With a user/password it
// performs AUTH PLAIN. TLS is implicit (SMTPS) when enabled — use port 465.
type smtpProtocol struct{}

func (smtpProtocol) Name() string       { return "smtp" }
func (smtpProtocol) DefaultPort() int   { return 25 }
func (smtpProtocol) RequiresUser() bool { return false }

func (smtpProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	c, err := dialDeadline(ctx, cfg, 25)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	return smtpHandshake(c, cfg)
}

// smtpHandshake reads the 220 greeting, greets with EHLO (falling back to HELO),
// authenticates with AUTH PLAIN when a user is supplied, and quits.
func smtpHandshake(rw io.ReadWriter, cfg Config) (Result, error) {
	br := bufio.NewReader(rw)
	code, greeting, err := readReplyCode(br)
	if err != nil {
		return Result{}, err
	}
	res := Result{Extra: map[string]string{"greeting": greeting}}
	if code != 220 {
		return Result{}, fmt.Errorf("unexpected greeting: %d %s", code, greeting)
	}

	if _, err := fmt.Fprint(rw, "EHLO sermo\r\n"); err != nil {
		return Result{}, err
	}
	code, _, err = readReplyCode(br)
	if err != nil {
		return Result{}, err
	}
	if code != 250 {
		// Older servers may not support EHLO; try HELO.
		if _, err := fmt.Fprint(rw, "HELO sermo\r\n"); err != nil {
			return Result{}, err
		}
		var text string
		if code, text, err = readReplyCode(br); err != nil {
			return Result{}, err
		}
		if code != 250 {
			return Result{}, fmt.Errorf("EHLO/HELO refused: %d %s", code, text)
		}
	}

	if cfg.User != "" {
		token := base64.StdEncoding.EncodeToString([]byte("\x00" + cfg.User + "\x00" + cfg.Password))
		if _, err := fmt.Fprintf(rw, "AUTH PLAIN %s\r\n", token); err != nil {
			return Result{}, err
		}
		code, text, err := readReplyCode(br)
		if err != nil {
			return Result{}, err
		}
		if code != 235 {
			return Result{}, fmt.Errorf("auth failed: %d %s", code, text)
		}
	}

	_, _ = fmt.Fprint(rw, "QUIT\r\n") // best effort
	return res, nil
}

// readReplyCode reads one (possibly multi-line) reply and returns its numeric
// code and the joined text. Lines with a '-' after the code continue the reply;
// a space ends it. This is the RFC 959 reply format, shared by SMTP and FTP.
func readReplyCode(br *bufio.Reader) (int, string, error) {
	var parts []string
	for {
		s, err := br.ReadString('\n')
		if err != nil {
			return 0, "", err
		}
		line := strings.TrimRight(s, "\r\n")
		if len(line) < 3 {
			return 0, "", fmt.Errorf("malformed reply %q", line)
		}
		code, err := strconv.Atoi(line[:3])
		if err != nil {
			return 0, "", fmt.Errorf("malformed reply code %q", line)
		}
		if len(line) > 4 {
			parts = append(parts, line[4:])
		}
		if len(line) < 4 || line[3] != '-' {
			return code, strings.TrimSpace(strings.Join(parts, " ")), nil
		}
	}
}
