package conn

import (
	"context"
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

const (
	spamdCommandPing = "PING SPAMC/1.5\r\n\r\n"
	spamdReplyPong   = "PONG"
	spamdReplyPrefix = "SPAMD/"
)

func (spamdProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	return probeLineCommand(ctx, cfg, defaultPortSpamd, spamdCommandPing, func(line string) (Result, bool) {
		version, ok := parseSpamdPong(line)
		return Result{Extra: map[string]string{extraProtocol: version, extraPing: respPong}}, ok
	}, "not a spamd PONG reply: %q")
}

// parseSpamdPong validates a spamd PING reply ("SPAMD/1.5 0 PONG") and returns
// the SPAMD protocol version.
func parseSpamdPong(line string) (string, bool) {
	if !strings.HasPrefix(line, spamdReplyPrefix) || !strings.Contains(line, spamdReplyPong) {
		return "", false
	}
	v := line[len(spamdReplyPrefix):]
	if i := strings.IndexByte(v, ' '); i >= 0 {
		v = v[:i]
	}
	return v, true
}
