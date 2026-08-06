package conn

import (
	"context"
	"fmt"
	"io"
	"strconv"
)

// nntpProtocol probes an NNTP news server natively (RFC 3977). With no user it is
// an anonymous connectivity check (verify the server greets 200/201). With a
// user/password it performs AUTHINFO USER/PASS authentication (RFC 4643). TLS is
// implicit (NNTPS) when enabled — use port 563.
type nntpProtocol struct{}

func (nntpProtocol) Name() string       { return ProtocolNameNNTP }
func (nntpProtocol) DefaultPort() int   { return defaultPortNNTP }
func (nntpProtocol) RequiresUser() bool { return false }

const (
	nntpCommandAuthInfoPassFormat = "AUTHINFO PASS %s\r\n" //nolint:gosec // G101: PASS is the NNTP AUTHINFO command verb; the credential is the %s argument.
	nntpCommandAuthInfoUserFormat = "AUTHINFO USER %s\r\n"
	nntpCommandQuit               = "QUIT\r\n"
	nntpExtraPostingAllowed       = "posting_allowed"
	nntpStatusAuthAccepted        = 281
	nntpStatusPasswordRequired    = 381
	nntpStatusPostingAllowed      = 200
	nntpStatusPostingProhibited   = 201
)

func (nntpProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	return probeBanner(ctx, cfg, defaultPortNNTP, nntpHandshake)
}

// nntpHandshake reads the greeting (200 posting allowed / 201 posting
// prohibited), authenticates with AUTHINFO USER/PASS when a user is supplied, and
// quits. NNTP shares the 3-digit status-line format parsed by net/textproto.
func nntpHandshake(rw io.ReadWriter, cfg Config) (Result, error) {
	tp, code, greeting, err := readTextGreeting(rw)
	if err != nil {
		return Result{}, err
	}
	if code != nntpStatusPostingAllowed && code != nntpStatusPostingProhibited {
		return Result{}, unexpectedGreeting(code, greeting)
	}
	res := Result{Extra: map[string]string{
		extraGreeting:           greeting,
		nntpExtraPostingAllowed: strconv.FormatBool(code == nntpStatusPostingAllowed),
	}}

	if cfg.User != "" {
		code, text, err := sendTextCommand(rw, tp, fmt.Sprintf(nntpCommandAuthInfoUserFormat, cfg.User))
		if err != nil {
			return Result{}, err
		}
		if code == nntpStatusPasswordRequired {
			if code, text, err = sendTextCommand(rw, tp, fmt.Sprintf(nntpCommandAuthInfoPassFormat, cfg.Password)); err != nil {
				return Result{}, err
			}
		}
		if code != nntpStatusAuthAccepted {
			return Result{}, fmt.Errorf("auth failed: %d %s", code, text)
		}
	}

	_, _ = fmt.Fprint(rw, nntpCommandQuit) // best effort
	return res, nil
}
