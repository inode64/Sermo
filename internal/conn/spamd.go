package conn

import (
	"context"
	"fmt"
	"io"
	"strings"
)

func init() { Register(spamdProtocol{}, protocolAliasSpamAssassin) }

// spamdProtocol probes the SpamAssassin daemon (spamd) over the SPAMC/SPAMD
// protocol. It sends a `PING` and verifies spamd answers `SPAMD/<v> 0 PONG` —
// proof it is up and speaking the protocol. spamd listens on TCP (default 783)
// or a Unix socket (set `socket`). No auth.
type spamdProtocol struct{}

func (spamdProtocol) Name() string       { return ProtocolNameSpamd }
func (spamdProtocol) DefaultPort() int   { return defaultPortSpamd }
func (spamdProtocol) RequiresUser() bool { return false }

func (spamdProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	c, err := dialDeadline(ctx, cfg, defaultPortSpamd)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()

	if _, err := io.WriteString(c, "PING SPAMC/1.5\r\n\r\n"); err != nil {
		return Result{}, err
	}
	line, err := readGreetingLine(c)
	if err != nil {
		return Result{}, err
	}
	version, ok := parseSpamdPong(line)
	if !ok {
		return Result{}, fmt.Errorf("not a spamd PONG reply: %q", line)
	}
	return Result{Extra: map[string]string{extraProtocol: version, extraPing: respPong}}, nil
}

// parseSpamdPong validates a spamd PING reply ("SPAMD/1.5 0 PONG") and returns
// the SPAMD protocol version.
func parseSpamdPong(line string) (string, bool) {
	const prefix = "SPAMD/"
	if !strings.HasPrefix(line, prefix) || !strings.Contains(line, "PONG") {
		return "", false
	}
	v := line[len(prefix):]
	if i := strings.IndexByte(v, ' '); i >= 0 {
		v = v[:i]
	}
	return v, true
}
