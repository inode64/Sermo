package conn

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
)

func init() { Register(nntpProtocol{}, "nntps") }

// nntpProtocol probes an NNTP news server natively (RFC 3977). With no user it is
// an anonymous connectivity check (verify the server greets 200/201). With a
// user/password it performs AUTHINFO USER/PASS authentication (RFC 4643). TLS is
// implicit (NNTPS) when enabled — use port 563.
type nntpProtocol struct{}

func (nntpProtocol) Name() string       { return "nntp" }
func (nntpProtocol) DefaultPort() int   { return 119 }
func (nntpProtocol) RequiresUser() bool { return false }

func (nntpProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	port := cfg.Port
	if port == 0 {
		port = 119
	}
	c, err := dialConn(ctx, cfg, port)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	return nntpHandshake(c, cfg)
}

// nntpHandshake reads the greeting (200 posting allowed / 201 posting
// prohibited), authenticates with AUTHINFO USER/PASS when a user is supplied, and
// quits. Reuses the RFC 959 reply reader (NNTP shares its 3-digit reply codes).
func nntpHandshake(rw io.ReadWriter, cfg Config) (Result, error) {
	br := bufio.NewReader(rw)
	code, greeting, err := readReplyCode(br)
	if err != nil {
		return Result{}, err
	}
	if code != 200 && code != 201 {
		return Result{}, fmt.Errorf("unexpected greeting: %d %s", code, greeting)
	}
	res := Result{Extra: map[string]string{
		"greeting":        greeting,
		"posting_allowed": strconv.FormatBool(code == 200),
	}}

	if cfg.User != "" {
		if _, err := fmt.Fprintf(rw, "AUTHINFO USER %s\r\n", cfg.User); err != nil {
			return Result{}, err
		}
		code, text, err := readReplyCode(br)
		if err != nil {
			return Result{}, err
		}
		if code == 381 { // 381: password required
			if _, err := fmt.Fprintf(rw, "AUTHINFO PASS %s\r\n", cfg.Password); err != nil {
				return Result{}, err
			}
			if code, text, err = readReplyCode(br); err != nil {
				return Result{}, err
			}
		}
		if code != 281 { // 281: authentication accepted
			return Result{}, fmt.Errorf("auth failed: %d %s", code, text)
		}
	}

	_, _ = fmt.Fprint(rw, "QUIT\r\n") // best effort
	return res, nil
}
