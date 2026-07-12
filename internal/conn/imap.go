package conn

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
)

func init() { Register(imapProtocol{}) }

const (
	imapGreetingOKPrefix      = "* OK"
	imapGreetingPreauthPrefix = "* PREAUTH"
	imapLoginCommand          = "LOGIN"
	imapLogoutCommand         = "LOGOUT"
	imapStatusOK              = "OK"
	imapStatusFieldIndex      = 0
	imapTagLogin              = "a1"
	imapTagLogout             = "a2"
	imapTerminator            = "\r\n"
)

// imapProtocol probes an IMAP server natively (RFC 3501). With no credentials it
// is an anonymous connectivity check: it reads the greeting and verifies the
// server is ready. With a user/password it performs a LOGIN. TLS is implicit
// (IMAPS) when enabled — use port 993.
type imapProtocol struct{}

func (imapProtocol) Name() string       { return ProtocolNameIMAP }
func (imapProtocol) DefaultPort() int   { return defaultPortIMAP }
func (imapProtocol) RequiresUser() bool { return false }

func (imapProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	return probeBanner(ctx, cfg, defaultPortIMAP, imapHandshake)
}

// imapHandshake reads the server greeting (which must be OK or PREAUTH), performs
// a LOGIN when credentials are supplied (and the server is not already
// pre-authenticated), and logs out. The greeting banner is returned in Extra.
func imapHandshake(rw io.ReadWriter, cfg Config) (Result, error) {
	br := bufio.NewReader(rw)
	greeting, err := readCRLFLine(br)
	if err != nil {
		return Result{}, err
	}
	res := Result{Extra: map[string]string{extraGreeting: strings.TrimSpace(greeting)}}

	preauth := strings.HasPrefix(greeting, imapGreetingPreauthPrefix)
	if !strings.HasPrefix(greeting, imapGreetingOKPrefix) && !preauth {
		return Result{}, fmt.Errorf("unexpected greeting: %s", strings.TrimSpace(greeting))
	}

	if (cfg.User != "" || cfg.Password != "") && !preauth {
		if _, err := fmt.Fprintf(rw, "%s %s %s %s%s", imapTagLogin, imapLoginCommand, imapQuote(cfg.User), imapQuote(cfg.Password), imapTerminator); err != nil {
			return Result{}, err
		}
		ok, status, err := readIMAPTagged(br, imapTagLogin)
		if err != nil {
			return Result{}, err
		}
		if !ok {
			return Result{}, fmt.Errorf("login failed: %s", status)
		}
	}

	// Best-effort logout (reply ignored).
	_, _ = fmt.Fprintf(rw, "%s %s%s", imapTagLogout, imapLogoutCommand, imapTerminator)
	return res, nil
}

// imapQuote renders s as an IMAP quoted string, escaping backslash and quote.
func imapQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// readIMAPTagged reads response lines until the one tagged with tag, reporting
// whether its status is OK and the full status line. Untagged ("*") lines are
// skipped.
func readIMAPTagged(br *bufio.Reader, tag string) (ok bool, status string, err error) {
	for {
		line, err := readCRLFLine(br)
		if err != nil {
			return false, "", err
		}
		if after, ok0 := strings.CutPrefix(line, tag+" "); ok0 {
			rest := strings.TrimSpace(after)
			fields := strings.Fields(rest)
			if len(fields) == 0 {
				return false, line, nil
			}
			return strings.EqualFold(fields[imapStatusFieldIndex], imapStatusOK), rest, nil
		}
		// otherwise an untagged "*" line — keep reading.
	}
}
