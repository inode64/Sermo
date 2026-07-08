package conn

import (
	"context"
	"fmt"
	"strings"
)

func init() { Register(rsyncProtocol{}, protocolAliasRsyncd) }

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
	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}
	port := cfg.Port
	if port == 0 {
		port = defaultPortRsync
	}
	c, err := BindDialer(cfg.Interface).DialContext(ctx, networkTCP, hostPort(host, port))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	applyDeadline(ctx, c)

	line, err := readGreetingLine(c)
	if err != nil {
		return Result{}, err
	}
	version, ok := rsyncGreetingVersion(line)
	if !ok {
		return Result{}, fmt.Errorf("not an rsync daemon: %q", line)
	}
	return Result{
		Version: version,
		Extra:   map[string]string{extraGreeting: line, extraProtocol: version},
	}, nil
}

// rsyncGreetingVersion extracts the protocol version from an rsync daemon
// greeting ("@RSYNCD: <version>").
func rsyncGreetingVersion(line string) (string, bool) {
	if !strings.HasPrefix(line, rsyncGreetingPrefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, rsyncGreetingPrefix)), true
}
