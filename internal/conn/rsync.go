package conn

import (
	"context"
	"strings"
)

// rsyncProtocol probes an rsync daemon natively. On connect, rsyncd sends an
// "@RSYNCD: <version>" greeting; reading it verifies the daemon is up and
// speaking the rsync protocol. No authentication (module access may need auth,
// but the greeting is unauthenticated).
type rsyncProtocol struct{}

func (rsyncProtocol) Name() string       { return ProtocolNameRsync }
func (rsyncProtocol) DefaultPort() int   { return defaultPortRsync }
func (rsyncProtocol) RequiresUser() bool { return false }

const rsyncGreetingPrefix = "@RSYNCD:"

func (rsyncProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	return probeLineCommand(ctx, cfg, defaultPortRsync, "", func(line string) (Result, bool) {
		version, ok := rsyncGreetingVersion(line)
		return Result{
			Version: version,
			Extra:   map[string]string{extraGreeting: line, extraProtocol: version},
		}, ok
	}, "not an rsync daemon: %q")
}

// rsyncGreetingVersion extracts the protocol version from an rsync daemon
// greeting ("@RSYNCD: <version>").
func rsyncGreetingVersion(line string) (string, bool) {
	if !strings.HasPrefix(line, rsyncGreetingPrefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, rsyncGreetingPrefix)), true
}
