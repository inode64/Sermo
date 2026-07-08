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

func init() {
	// Valkey is a redis fork speaking the same RESP protocol.
	Register(redisProtocol{}, protocolAliasValkey)
}

const (
	redisCommandAuth         = "AUTH"
	redisCommandInfo         = "INFO"
	redisCommandPing         = "PING"
	redisInfoVersion         = "redis_version"
	redisInfoUptimeInSeconds = "uptime_in_seconds"
	redisPong                = "PONG"
)

const (
	redisInfoAOFLastWriteStatus = "aof_last_write_status"
	redisInfoConnectedClients   = "connected_clients"
	redisInfoLoading            = "loading"
	redisInfoMasterLinkStatus   = "master_link_status"
	redisInfoMaxMemory          = "maxmemory"
	redisInfoMemFragRatio       = "mem_fragmentation_ratio"
	redisInfoRDBLastSaveStatus  = "rdb_last_bgsave_status"
	redisInfoUsedMemory         = "used_memory"
)

const (
	redisInfoCommentPrefix       = "#"
	redisInfoFieldSeparator      = ":"
	redisInfoLineSeparator       = "\n"
	redisInfoTrimRight           = "\r"
	redisRESPArrayHeaderFormat   = "*%d\r\n"
	redisRESPBulkStringFormat    = "$%d\r\n%s\r\n"
	redisRESPPayloadOffset       = 1
	redisRESPBulkTerminatorBytes = 2
	redisRESPTypeOffset          = 0
	redisRESPTypeBulkString      = '$'
	redisRESPTypeError           = '-'
	redisRESPTypeInteger         = ':'
	redisRESPTypeSimpleString    = '+'
)

// redisProtocol probes a Redis (or Valkey) server natively over RESP — no
// external driver: the handshake (optional AUTH, then PING, then INFO for the
// version) is a few simple commands, so per the native-Go-first convention it is
// implemented directly on a socket.
type redisProtocol struct{}

func (redisProtocol) Name() string       { return ProtocolNameRedis }
func (redisProtocol) DefaultPort() int   { return defaultPortRedis }
func (redisProtocol) RequiresUser() bool { return false }

// Probe connects (over TLS when configured), authenticates if a password/user is
// set, verifies the server answers PING, and reads its version. The caller's
// context bounds the probe.
func (redisProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	return probeBanner(ctx, cfg, defaultPortRedis, redisHandshake)
}

// redisHandshake runs the RESP handshake on rw: optional AUTH, a PING that must
// answer PONG, then a best-effort INFO for the server version.
func redisHandshake(rw io.ReadWriter, cfg Config) (Result, error) {
	br := bufio.NewReader(rw)

	if cfg.Password != "" || cfg.User != "" {
		args := []string{redisCommandAuth}
		if cfg.User != "" {
			args = append(args, cfg.User)
		}
		args = append(args, cfg.Password)
		if err := writeRESP(rw, args...); err != nil {
			return Result{}, err
		}
		if _, err := readRESP(br); err != nil {
			return Result{}, fmt.Errorf("auth: %w", err)
		}
	}

	if err := writeRESP(rw, redisCommandPing); err != nil {
		return Result{}, err
	}
	pong, err := readRESP(br)
	if err != nil {
		return Result{}, err
	}
	if !strings.EqualFold(pong, redisPong) {
		return Result{}, fmt.Errorf("unexpected PING reply %q", pong)
	}

	// Server identity and health: best effort; a successful PING already proves
	// connect + auth. A single INFO carries version plus role, replication,
	// persistence and memory fields, each exposed in Extra so an expect: rule can
	// assert on it (e.g. role == master, master_link_status == up,
	// rdb_last_bgsave_status == ok).
	res := Result{Extra: map[string]string{}}
	if writeRESP(rw, redisCommandInfo) == nil {
		if info, err := readRESP(br); err == nil {
			fields := parseRedisInfo(info)
			res.Version = fields[redisInfoVersion]
			for _, k := range []string{
				ExtraKeyRole, redisInfoMasterLinkStatus, redisInfoConnectedClients,
				redisInfoUsedMemory, redisInfoMaxMemory, redisInfoMemFragRatio,
				redisInfoRDBLastSaveStatus, redisInfoAOFLastWriteStatus, redisInfoLoading,
			} {
				if v := fields[k]; v != "" {
					res.Extra[k] = v
				}
			}
			if v := fields[redisInfoUptimeInSeconds]; v != "" {
				res.Extra[extraUptime] = v
			}
		}
	}
	return res, nil
}

// writeRESP encodes args as a RESP array of bulk strings.
func writeRESP(w io.Writer, args ...string) error {
	var b strings.Builder
	fmt.Fprintf(&b, redisRESPArrayHeaderFormat, len(args))
	for _, a := range args {
		fmt.Fprintf(&b, redisRESPBulkStringFormat, len(a), a)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// readRESP reads one reply: simple string (+), integer (:) and bulk string ($)
// return their payload; an error reply (-) returns it as a Go error. Any other
// type byte (RESP arrays, RESP3 aggregates) is an explicit error rather than a
// silently mis-stripped payload — the handshake only issues commands that answer
// with the scalar types above.
func readRESP(br *bufio.Reader) (string, error) {
	line, err := readCRLFLine(br)
	if err != nil {
		return "", err
	}
	if line == "" {
		return "", errors.New("empty reply")
	}
	switch line[redisRESPTypeOffset] {
	case redisRESPTypeSimpleString, redisRESPTypeInteger:
		return line[redisRESPPayloadOffset:], nil
	case redisRESPTypeError:
		return "", errors.New(line[redisRESPPayloadOffset:])
	case redisRESPTypeBulkString:
		n, err := strconv.Atoi(line[redisRESPPayloadOffset:])
		if err != nil {
			return "", fmt.Errorf("bad bulk length %q", line)
		}
		if n < 0 {
			return "", nil // null bulk
		}
		buf := make([]byte, n+redisRESPBulkTerminatorBytes)
		if _, err := io.ReadFull(br, buf); err != nil {
			return "", err
		}
		return string(buf[:n]), nil
	default:
		return "", fmt.Errorf("unsupported RESP reply type %q", string(line[redisRESPTypeOffset]))
	}
}

// parseRedisInfo parses an INFO reply — "key:value" lines, "# Section" headers
// and blank separators — into a flat map. Section headers and blanks are
// dropped; each field is split on its first ':'.
func parseRedisInfo(info string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(info, redisInfoLineSeparator) {
		line = strings.TrimRight(line, redisInfoTrimRight)
		if line == "" || strings.HasPrefix(line, redisInfoCommentPrefix) {
			continue
		}
		if k, v, ok := strings.Cut(line, redisInfoFieldSeparator); ok {
			out[k] = v
		}
	}
	return out
}
