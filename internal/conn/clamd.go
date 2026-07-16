package conn

import (
	"context"
	"strings"
)

func init() { Register(clamdProtocol{}, protocolAliasClamAV) }

// clamdProtocol probes a ClamAV daemon (clamd) natively over its simple text
// protocol. It sends the `VERSION` command (newline-prefixed form) and verifies
// a `ClamAV <version>/…` reply — proof the daemon is up and speaking the clamd
// protocol — extracting the engine version. clamd listens on a Unix socket (set
// `socket`) or TCP (default port 3310). No auth, no TLS.
type clamdProtocol struct{}

func (clamdProtocol) Name() string       { return ProtocolNameClamd }
func (clamdProtocol) DefaultPort() int   { return defaultPortClamd }
func (clamdProtocol) RequiresUser() bool { return false }

const (
	clamdCommandVersion      = "nVERSION\n"
	clamdVersionEngineSep    = '/'
	clamdVersionStringPrefix = "ClamAV "
)

func (clamdProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	return probeLineCommand(ctx, cfg, defaultPortClamd, clamdCommandVersion, func(line string) (Result, bool) {
		version, ok := clamdVersion(line)
		return Result{Version: version, Extra: map[string]string{ExtraKeyVersionString: line}}, ok
	}, "not a clamd VERSION reply: %q")
}

// clamdVersion extracts the engine version from a clamd VERSION reply
// ("ClamAV 0.103.8/26900/Wed Mar 15 …" -> "0.103.8"). Returning just the engine
// version (not the daily signature database part) keeps on_version_change quiet
// across routine database updates.
func clamdVersion(line string) (string, bool) {
	if !strings.HasPrefix(line, clamdVersionStringPrefix) {
		return "", false
	}
	v := strings.TrimPrefix(line, clamdVersionStringPrefix)
	if i := strings.IndexByte(v, clamdVersionEngineSep); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v), true
}
