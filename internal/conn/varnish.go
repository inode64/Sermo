package conn

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	maxVarnishCLIBody       = 1 << 16
	varnishStatusAuthNeeded = 107 // CLIS_AUTH
	varnishStatusOK         = 200 // CLIS_OK
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
	c, err := newProbeTarget(cfg, defaultPortVarnish).openStream(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()

	br := bufio.NewReader(c)
	line, err := readCRLFLineLenient(br)
	if err != nil {
		return Result{}, probeErr(ProtocolNameVarnish, stepVarnishCLIBanner, err)
	}
	status, length, err := parseVarnishStatus(line)
	if err != nil {
		return Result{}, probeErr(ProtocolNameVarnish, stepVarnishCLIStatus, err)
	}
	// Only the banner (200) and the auth challenge (107) prove a working CLI;
	// e.g. 400 CLIS_COMMS or 500 CLIS_CLOSE mean varnishd is refusing it.
	if status != varnishStatusOK && status != varnishStatusAuthNeeded {
		return Result{}, fmt.Errorf("varnish: CLI answered status %d", status)
	}
	if length > maxVarnishCLIBody {
		return Result{}, fmt.Errorf("varnish: implausible CLI body length %d", length)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(br, buf); err != nil {
		return Result{}, probeErr(ProtocolNameVarnish, stepResponseBody, err)
	}
	body := string(buf)

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
