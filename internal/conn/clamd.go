package conn

import (
	"context"
	"fmt"
	"io"
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

func (clamdProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	c, err := dialDeadline(ctx, cfg, defaultPortClamd)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()

	if _, err := io.WriteString(c, "nVERSION\n"); err != nil {
		return Result{}, err
	}
	line, err := readGreetingLine(c)
	if err != nil {
		return Result{}, err
	}
	version, ok := clamdVersion(line)
	if !ok {
		return Result{}, fmt.Errorf("not a clamd VERSION reply: %q", line)
	}
	return Result{Version: version, Extra: map[string]string{ExtraKeyVersionString: line}}, nil
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
