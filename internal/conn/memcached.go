package conn

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

func init() {
	// "memcache" is a common shorthand for the same daemon.
	Register(memcachedProtocol{}, protocolAliasMemcache)
}

// memcachedProtocol probes a memcached server natively over its ASCII text
// protocol — no external driver. A single `stats` command both proves the daemon
// is up (it answers `STAT <key> <value>` lines terminated by `END`) and carries
// the server version plus operational counters, each exposed in Result.Extra so
// an expect: rule can assert on them.
type memcachedProtocol struct{}

func (memcachedProtocol) Name() string       { return ProtocolNameMemcached }
func (memcachedProtocol) DefaultPort() int   { return defaultPortMemcached }
func (memcachedProtocol) RequiresUser() bool { return false }

const (
	memcachedCommandStats            = "stats\r\n"
	memcachedFieldSeparator          = " "
	memcachedReplyEnd                = "END"
	memcachedReplyStatPrefix         = "STAT "
	memcachedStatBytes               = "bytes"
	memcachedStatCmdGet              = "cmd_get"
	memcachedStatCmdSet              = "cmd_set"
	memcachedStatCurrConnections     = "curr_connections"
	memcachedStatCurrItems           = "curr_items"
	memcachedStatEvictions           = "evictions"
	memcachedStatGetHits             = "get_hits"
	memcachedStatGetMisses           = "get_misses"
	memcachedStatLimitMaxBytes       = "limit_maxbytes"
	memcachedStatRejectedConnections = "rejected_connections"
	memcachedStatThreads             = "threads"
	memcachedStatTotalConnections    = "total_connections"
	memcachedStatTotalItems          = "total_items"
	memcachedStatUptime              = "uptime"
	memcachedStatVersion             = "version"
)

// Probe connects (TCP, a Unix socket when cfg.Socket is set, TLS when
// configured) and runs the stats handshake. The caller's context bounds it.
func (memcachedProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	c, err := dialDeadline(ctx, cfg, defaultPortMemcached)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	return memcachedStats(c)
}

// memcachedStats sends `stats` and parses the `STAT <key> <value>` lines up to
// the terminating `END`. It publishes the version and a curated set of counters
// in Result.Extra; any non-STAT/non-END line (e.g. an ERROR reply, or a server
// that is not memcached) fails the probe.
func memcachedStats(rw io.ReadWriter) (Result, error) {
	if _, err := io.WriteString(rw, memcachedCommandStats); err != nil {
		return Result{}, err
	}
	br := bufio.NewReader(rw)
	fields := map[string]string{}
	for {
		line, err := readCRLFLine(br)
		if err != nil {
			return Result{}, err
		}
		if line == memcachedReplyEnd {
			break
		}
		rest, ok := strings.CutPrefix(line, memcachedReplyStatPrefix)
		if !ok {
			return Result{}, fmt.Errorf("unexpected memcached stats reply %q", line)
		}
		if k, v, ok := strings.Cut(rest, memcachedFieldSeparator); ok {
			fields[k] = v
		}
	}
	if len(fields) == 0 {
		return Result{}, errors.New("memcached returned no stats")
	}

	// version is reported via Result.Version (assertable as expect.version); the
	// rest are numeric counters/gauges worth asserting on — connections, the
	// hit-ratio inputs, capacity and evictions — published as numeric strings so
	// expect: can use >, <, == against them.
	res := Result{Version: fields[memcachedStatVersion], Extra: map[string]string{}}
	for _, k := range []string{
		memcachedStatUptime,
		memcachedStatCurrConnections,
		memcachedStatTotalConnections,
		memcachedStatRejectedConnections,
		memcachedStatCmdGet,
		memcachedStatCmdSet,
		memcachedStatGetHits,
		memcachedStatGetMisses,
		memcachedStatCurrItems,
		memcachedStatTotalItems,
		memcachedStatBytes,
		memcachedStatEvictions,
		memcachedStatLimitMaxBytes,
		memcachedStatThreads,
	} {
		if v := fields[k]; v != "" {
			res.Extra[k] = v
		}
	}
	return res, nil
}
