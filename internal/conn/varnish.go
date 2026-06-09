package conn

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
)

func init() { Register(varnishProtocol{}, "varnishadm") }

// varnishProtocol probes Varnish Cache via its management CLI (varnishadm, the
// `-T` admin port). On connect varnishd sends a CLI response: a "<status>
// <length>" line followed by a body of that length. Status 200 carries the
// banner (with the version); status 107 is an authentication challenge (a secret
// is configured). Either proves the management CLI is up and speaking the
// protocol. Liveness only — the CLI secret authentication is not performed.
type varnishProtocol struct{}

func (varnishProtocol) Name() string       { return "varnish" }
func (varnishProtocol) DefaultPort() int   { return 6082 }
func (varnishProtocol) RequiresUser() bool { return false }

func (varnishProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	port := cfg.Port
	if port == 0 {
		port = 6082
	}
	c, err := dialConn(ctx, cfg.Host, port, cfg.TLS)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}

	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil && line == "" {
		return Result{}, err
	}
	status, length, err := parseVarnishStatus(line)
	if err != nil {
		return Result{}, err
	}
	body := ""
	if length > 0 && length <= 1<<16 {
		buf := make([]byte, length)
		if _, rerr := io.ReadFull(br, buf); rerr == nil {
			body = string(buf)
		}
	}

	extra := map[string]string{"cli_status": strconv.Itoa(status)}
	if status == 107 { // CLIS_AUTH
		extra["auth_required"] = "true"
	}
	return Result{Version: varnishVersion(body), Extra: extra}, nil
}

// parseVarnishStatus parses a Varnish CLI status line ("<status> <length>").
func parseVarnishStatus(line string) (status, length int, err error) {
	f := strings.Fields(line)
	if len(f) < 2 {
		return 0, 0, errors.New("not a Varnish CLI status line")
	}
	if status, err = strconv.Atoi(f[0]); err != nil {
		return 0, 0, errors.New("invalid Varnish CLI status")
	}
	if length, err = strconv.Atoi(f[1]); err != nil || length < 0 {
		return 0, 0, errors.New("invalid Varnish CLI length")
	}
	return status, length, nil
}

// varnishVersion extracts the version from a CLI banner ("varnish-7.4.1
// revision …" -> "7.4.1"). Empty when absent (e.g. an auth challenge).
func varnishVersion(body string) string {
	const prefix = "varnish-"
	i := strings.Index(body, prefix)
	if i < 0 {
		return ""
	}
	v := body[i+len(prefix):]
	if j := strings.IndexAny(v, " \t\r\n"); j >= 0 {
		v = v[:j]
	}
	return v
}
