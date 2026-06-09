package conn

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
)

func init() { Register(clamdProtocol{}, "clamav") }

// clamdProtocol probes a ClamAV daemon (clamd) natively over its simple text
// protocol. It sends the `VERSION` command (newline-prefixed form) and verifies
// a `ClamAV <version>/…` reply — proof the daemon is up and speaking the clamd
// protocol — extracting the engine version. clamd listens on a Unix socket (set
// `socket`) or TCP (default port 3310). No auth, no TLS.
type clamdProtocol struct{}

func (clamdProtocol) Name() string       { return "clamd" }
func (clamdProtocol) DefaultPort() int   { return 3310 }
func (clamdProtocol) RequiresUser() bool { return false }

func (clamdProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	var (
		c   net.Conn
		err error
	)
	if cfg.Socket != "" {
		c, err = (&net.Dialer{}).DialContext(ctx, "unix", cfg.Socket)
	} else {
		port := cfg.Port
		if port == 0 {
			port = 3310
		}
		c, err = dialConn(ctx, cfg, port)
	}
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}

	if _, err := io.WriteString(c, "nVERSION\n"); err != nil {
		return Result{}, err
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil && line == "" {
		return Result{}, err
	}
	line = strings.TrimRight(line, "\r\n")
	version, ok := clamdVersion(line)
	if !ok {
		return Result{}, fmt.Errorf("not a clamd VERSION reply: %q", line)
	}
	return Result{Version: version, Extra: map[string]string{"version_string": line}}, nil
}

// clamdVersion extracts the engine version from a clamd VERSION reply
// ("ClamAV 0.103.8/26900/Wed Mar 15 …" -> "0.103.8"). Returning just the engine
// version (not the daily signature database part) keeps on_version_change quiet
// across routine database updates.
func clamdVersion(line string) (string, bool) {
	const prefix = "ClamAV "
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	v := strings.TrimPrefix(line, prefix)
	if i := strings.IndexByte(v, '/'); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v), true
}
