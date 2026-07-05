package conn

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
)

func init() { Register(imapProtocol{}) }

// imapProtocol probes an IMAP server natively (RFC 3501). With no credentials it
// is an anonymous connectivity check: it reads the greeting and verifies the
// server is ready. With a user/password it performs a LOGIN. TLS is implicit
// (IMAPS) when enabled — use port 993.
type imapProtocol struct{}

func (imapProtocol) Name() string       { return "imap" }
func (imapProtocol) DefaultPort() int   { return 143 }
func (imapProtocol) RequiresUser() bool { return false }

func (imapProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	return probeBanner(ctx, cfg, 143, imapHandshake)
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

	preauth := strings.HasPrefix(greeting, "* PREAUTH")
	if !strings.HasPrefix(greeting, "* OK") && !preauth {
		return Result{}, fmt.Errorf("unexpected greeting: %s", strings.TrimSpace(greeting))
	}

	if (cfg.User != "" || cfg.Password != "") && !preauth {
		const tag = "a1"
		if _, err := fmt.Fprintf(rw, "%s LOGIN %s %s\r\n", tag, imapQuote(cfg.User), imapQuote(cfg.Password)); err != nil {
			return Result{}, err
		}
		ok, status, err := readIMAPTagged(br, tag)
		if err != nil {
			return Result{}, err
		}
		if !ok {
			return Result{}, fmt.Errorf("login failed: %s", status)
		}
	}

	// Best-effort logout (reply ignored).
	_, _ = fmt.Fprint(rw, "a2 LOGOUT\r\n")
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
		if strings.HasPrefix(line, tag+" ") {
			rest := strings.TrimSpace(strings.TrimPrefix(line, tag+" "))
			fields := strings.Fields(rest)
			if len(fields) == 0 {
				return false, line, nil
			}
			return strings.EqualFold(fields[0], "OK"), rest, nil
		}
		// otherwise an untagged "*" line — keep reading.
	}
}
