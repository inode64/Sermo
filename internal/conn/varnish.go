package conn

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
)

func init() { Register(varnishProtocol{}, protocolAliasVarnishAdm) }

const (
	maxVarnishCLIBody       = 1 << 16
	varnishStatusAuthNeeded = 107 // CLIS_AUTH
	varnishStatusLineFields = 2
	varnishStatusFieldIndex = 0
	varnishLengthFieldIndex = 1
	varnishVersionDelims    = " \t\r\n"
	varnishVersionPrefix    = "varnish-"
	extraAuthRequired       = "auth_required"
)

// varnishProtocol probes Varnish Cache via its management CLI (varnishadm, the
// `-T` admin port). On connect varnishd sends a CLI response: a "<status>
// <length>" line followed by a body of that length. Status 200 carries the
// banner (with the version); status 107 is an authentication challenge (a secret
// is configured). Either proves the management CLI is up and speaking the
// protocol. Liveness only — the CLI secret authentication is not performed.
type varnishProtocol struct{}

func (varnishProtocol) Name() string       { return ProtocolNameVarnish }
func (varnishProtocol) DefaultPort() int   { return defaultPortVarnish }
func (varnishProtocol) RequiresUser() bool { return false }

func (varnishProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	c, err := dialDeadline(ctx, cfg, defaultPortVarnish)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()

	br := bufio.NewReader(c)
	line, err := br.ReadString(protocolLineBreak)
	if err != nil && line == "" {
		return Result{}, err
	}
	status, length, err := parseVarnishStatus(line)
	if err != nil {
		return Result{}, err
	}
	body := ""
	if length > 0 && length <= maxVarnishCLIBody {
		buf := make([]byte, length)
		if _, rerr := io.ReadFull(br, buf); rerr == nil {
			body = string(buf)
		}
	}

	extra := map[string]string{extraCLIStatus: strconv.Itoa(status)}
	if status == varnishStatusAuthNeeded {
		extra[extraAuthRequired] = strconv.FormatBool(true)
	}
	return Result{Version: varnishVersion(body), Extra: extra}, nil
}

// parseVarnishStatus parses a Varnish CLI status line ("<status> <length>").
func parseVarnishStatus(line string) (status, length int, err error) {
	f := strings.Fields(line)
	if len(f) < varnishStatusLineFields {
		return 0, 0, errors.New("not a Varnish CLI status line")
	}
	if status, err = strconv.Atoi(f[varnishStatusFieldIndex]); err != nil {
		return 0, 0, errors.New("invalid Varnish CLI status")
	}
	if length, err = strconv.Atoi(f[varnishLengthFieldIndex]); err != nil || length < 0 {
		return 0, 0, errors.New("invalid Varnish CLI length")
	}
	return status, length, nil
}

// varnishVersion extracts the version from a CLI banner ("varnish-7.4.1
// revision …" -> "7.4.1"). Empty when absent (e.g. an auth challenge).
func varnishVersion(body string) string {
	_, after, ok := strings.Cut(body, varnishVersionPrefix)
	if !ok {
		return ""
	}
	v := after
	if j := strings.IndexAny(v, varnishVersionDelims); j >= 0 {
		v = v[:j]
	}
	return v
}
