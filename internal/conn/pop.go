package conn

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

func init() { Register(popProtocol{}, protocolAliasPOP3) }

// popProtocol probes a POP3 server natively (RFC 1939). With no user it is an
// anonymous connectivity check (verify the server greets +OK). With a
// user/password it performs USER/PASS authentication. TLS is implicit (POP3S)
// when enabled — use port 995.
type popProtocol struct{}

func (popProtocol) Name() string       { return ProtocolNamePOP }
func (popProtocol) DefaultPort() int   { return defaultPortPOP }
func (popProtocol) RequiresUser() bool { return false }

func (popProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	return probeBanner(ctx, cfg, defaultPortPOP, popHandshake)
}

// popHandshake reads the +OK greeting, authenticates with USER/PASS when a user
// is supplied, and quits. POP3 has no password-only or pre-auth mode, so auth is
// gated on a username; with none it is an anonymous connectivity check.
func popHandshake(rw io.ReadWriter, cfg Config) (Result, error) {
	br := bufio.NewReader(rw)
	greeting, err := readPOPReply(br)
	if err != nil {
		return Result{}, fmt.Errorf("greeting: %w", err)
	}
	res := Result{Extra: map[string]string{extraGreeting: greeting}}

	if cfg.User != "" {
		if _, err := fmt.Fprintf(rw, "USER %s\r\n", cfg.User); err != nil {
			return Result{}, err
		}
		if _, err := readPOPReply(br); err != nil {
			return Result{}, fmt.Errorf("user: %w", err)
		}
		if _, err := fmt.Fprintf(rw, "PASS %s\r\n", cfg.Password); err != nil {
			return Result{}, err
		}
		if _, err := readPOPReply(br); err != nil {
			return Result{}, fmt.Errorf("pass: %w", err)
		}
	}

	_, _ = fmt.Fprint(rw, "QUIT\r\n") // best effort
	return res, nil
}

// readPOPReply reads one status line: "+OK <text>" returns the text; "-ERR
// <text>" returns it as an error.
func readPOPReply(br *bufio.Reader) (string, error) {
	s, err := br.ReadString('\n')
	if err != nil {
		return "", err
	}
	line := strings.TrimRight(s, "\r\n")
	switch {
	case strings.HasPrefix(line, "+OK"):
		return strings.TrimSpace(strings.TrimPrefix(line, "+OK")), nil
	case strings.HasPrefix(line, "-ERR"):
		return "", errors.New(strings.TrimSpace(strings.TrimPrefix(line, "-ERR")))
	default:
		return "", fmt.Errorf("unexpected reply: %s", line)
	}
}
