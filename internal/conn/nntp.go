package conn

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/textproto"
	"strconv"
)

func init() { Register(nntpProtocol{}, protocolAliasNNTPs) }

// nntpProtocol probes an NNTP news server natively (RFC 3977). With no user it is
// an anonymous connectivity check (verify the server greets 200/201). With a
// user/password it performs AUTHINFO USER/PASS authentication (RFC 4643). TLS is
// implicit (NNTPS) when enabled — use port 563.
type nntpProtocol struct{}

func (nntpProtocol) Name() string       { return ProtocolNameNNTP }
func (nntpProtocol) DefaultPort() int   { return defaultPortNNTP }
func (nntpProtocol) RequiresUser() bool { return false }

const (
	// #nosec G101 -- PASS is the NNTP AUTHINFO command verb, not a credential.
	nntpCommandAuthInfoPassFormat = "AUTHINFO PASS %s\r\n"
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
	tp := textproto.NewReader(bufio.NewReader(rw))
	code, greeting, err := tp.ReadResponse(0)
	if err != nil {
		return Result{}, err
	}
	if code != nntpStatusPostingAllowed && code != nntpStatusPostingProhibited {
		return Result{}, fmt.Errorf("unexpected greeting: %d %s", code, greeting)
	}
	res := Result{Extra: map[string]string{
		extraGreeting:           greeting,
		nntpExtraPostingAllowed: strconv.FormatBool(code == nntpStatusPostingAllowed),
	}}

	if cfg.User != "" {
		if _, err := fmt.Fprintf(rw, nntpCommandAuthInfoUserFormat, cfg.User); err != nil {
			return Result{}, err
		}
		code, text, err := tp.ReadResponse(0)
		if err != nil {
			return Result{}, err
		}
		if code == nntpStatusPasswordRequired {
			if _, err := fmt.Fprintf(rw, nntpCommandAuthInfoPassFormat, cfg.Password); err != nil {
				return Result{}, err
			}
			if code, text, err = tp.ReadResponse(0); err != nil {
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
