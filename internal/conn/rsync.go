package conn

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
)

func init() { Register(rsyncProtocol{}, "rsyncd") }

// rsyncProtocol probes an rsync daemon natively. On connect, rsyncd sends an
// "@RSYNCD: <version>" greeting; reading it verifies the daemon is up and
// speaking the rsync protocol. No authentication (module access may need auth,
// but the greeting is unauthenticated).
type rsyncProtocol struct{}

func (rsyncProtocol) Name() string       { return "rsync" }
func (rsyncProtocol) DefaultPort() int   { return 873 }
func (rsyncProtocol) RequiresUser() bool { return false }

func (rsyncProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 873
	}
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}

	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil && line == "" {
		return Result{}, err
	}
	line = strings.TrimRight(line, "\r\n")
	version, ok := rsyncGreetingVersion(line)
	if !ok {
		return Result{}, fmt.Errorf("not an rsync daemon: %q", line)
	}
	return Result{
		Version: version,
		Extra:   map[string]string{"greeting": line, "protocol": version},
	}, nil
}

// rsyncGreetingVersion extracts the protocol version from an rsync daemon
// greeting ("@RSYNCD: <version>").
func rsyncGreetingVersion(line string) (string, bool) {
	const prefix = "@RSYNCD:"
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, prefix)), true
}
