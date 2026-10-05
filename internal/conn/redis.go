package conn

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"sermo/internal/units"
)

const (
	maxRedisBulk             = units.BytesPerMiB
	redisCommandAuth         = "AUTH"
	redisCommandInfo         = "INFO"
	redisCommandPing         = "PING"
	redisInfoVersion         = "redis_version"
	redisInfoUptimeInSeconds = "uptime_in_seconds"
	redisPong                = "PONG"
)

const (
	redisInfoAOFLastWriteStatus = "aof_last_write_status"
	redisInfoEvictedKeys        = "evicted_keys"
	redisInfoLoading            = "loading"
	redisInfoMasterLinkStatus   = "master_link_status"
	redisInfoMaxMemory          = "maxmemory"
	redisInfoMaxMemoryPolicy    = "maxmemory_policy"
	redisInfoMemFragRatio       = "mem_fragmentation_ratio"
	redisInfoRDBLastSaveStatus  = "rdb_last_bgsave_status"
	redisInfoSyncFull           = "sync_full"
	redisInfoUsedMemory         = "used_memory"
)

// Derived fields: computed from INFO rather than copied, so an expect: rule can
// compare one number against a threshold (expect has no field-to-field form).
const (
	redisExtraKeys             = "keys"
	redisExtraMaxMemoryUsedPct = "maxmemory_used_pct"
	redisExtraRejectedCalls    = "rejected_calls"
)

// The probe reads INFO all: the default sections leave out commandstats (one
// "cmdstat_<name>:calls=…,rejected_calls=…,…" line per command), and several
// sections in one INFO need Redis 7, while "all" is understood by every
// Redis, Valkey and KeyDB — one round trip for the health fields and the
// rejected calls.
const (
	redisInfoSectionAll      = "all"
	redisCommandStatsPrefix  = "cmdstat_"
	redisRejectedCallsPrefix = "rejected_calls="
)

const (
	redisKeyspaceDBPrefix    = "db"
	redisKeyspaceKeysPrefix  = "keys="
	redisKeyspaceStatsSep    = ","
	redisMaxMemoryUnlimited  = "0"
	redisPercentScale        = 100
	redisPercentDecimals     = 2
	redisPercentFloatBitSize = 64
	redisCounterBitSize      = 64
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
	// connect + auth. A single INFO all carries version plus role, replication,
	// persistence, memory and command fields, each exposed in Extra so an
	// expect: rule can assert on it (e.g. role == master, master_link_status ==
	// up, rdb_last_bgsave_status == ok).
	res := Result{Extra: map[string]string{}}
	info, ok := redisRequest(rw, br, redisCommandInfo, redisInfoSectionAll)
	if !ok {
		return res, nil
	}
	fields := parseRedisInfo(info)
	res.Version = fields[redisInfoVersion]
	addRedisInfoExtra(res.Extra, fields)
	return res, nil
}

// redisRequest sends one best-effort command after the handshake and reads its
// reply; ok is false when either step failed (the probe has proven the server
// already, so a failed INFO only leaves the health fields out).
func redisRequest(rw io.ReadWriter, br *bufio.Reader, args ...string) (string, bool) {
	if writeRESP(rw, args...) != nil {
		return "", false
	}
	reply, err := readRESP(br)
	return reply, err == nil
}

// addRedisInfoExtra copies the INFO health fields and the derived ones into
// extra.
func addRedisInfoExtra(extra, fields map[string]string) {
	for _, k := range []string{
		ExtraKeyRole, redisInfoMasterLinkStatus, ExtraKeyConnectedClients,
		redisInfoUsedMemory, redisInfoMaxMemory, redisInfoMemFragRatio,
		redisInfoMaxMemoryPolicy, redisInfoEvictedKeys, redisInfoSyncFull,
		redisInfoRDBLastSaveStatus, redisInfoAOFLastWriteStatus, redisInfoLoading,
	} {
		if v := fields[k]; v != "" {
			extra[k] = v
		}
	}
	extra[redisExtraKeys] = strconv.FormatUint(redisKeyspaceKeys(fields), numericBaseDecimal)
	if pct, ok := redisMaxMemoryUsedPct(fields); ok {
		extra[redisExtraMaxMemoryUsedPct] = pct
	}
	if v := fields[redisInfoUptimeInSeconds]; v != "" {
		extra[extraUptime] = v
	}
	if total, ok := redisRejectedCalls(fields); ok {
		extra[redisExtraRejectedCalls] = strconv.FormatUint(total, numericBaseDecimal)
	}
}

// writeRESP encodes args as a RESP array of bulk strings.
func writeRESP(w io.Writer, args ...string) error {
	var b strings.Builder
	fmt.Fprintf(&b, redisRESPArrayHeaderFormat, len(args))
	for _, a := range args {
		fmt.Fprintf(&b, redisRESPBulkStringFormat, len(a), a)
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return probeErr(ProtocolNameRedis, stepRequest, err)
	}
	return nil
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
		if n == -1 {
			return "", nil // null bulk
		}
		if n < 0 || n > maxRedisBulk {
			return "", fmt.Errorf("redis bulk length %d outside 0..%d", n, maxRedisBulk)
		}
		buf := make([]byte, n+redisRESPBulkTerminatorBytes)
		if _, err := io.ReadFull(br, buf); err != nil {
			return "", probeErr(ProtocolNameRedis, stepRedisBulkString, err)
		}
		if string(buf[n:]) != "\r\n" {
			return "", errors.New("invalid Redis bulk terminator")
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
	for line := range strings.SplitSeq(info, redisInfoLineSeparator) {
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

// redisKeyspaceKeys sums the key count of every database in the INFO keyspace
// section ("db0:keys=12,expires=3,avg_ttl=0"). A server with no keys reports no
// dbN line at all, so the sum is 0 rather than a missing field.
func redisKeyspaceKeys(fields map[string]string) uint64 {
	var total uint64
	for name, stats := range fields {
		index, isDB := strings.CutPrefix(name, redisKeyspaceDBPrefix)
		if !isDB {
			continue
		}
		if _, err := strconv.ParseUint(index, numericBaseDecimal, strconv.IntSize); err != nil {
			continue
		}
		for stat := range strings.SplitSeq(stats, redisKeyspaceStatsSep) {
			raw, isKeys := strings.CutPrefix(stat, redisKeyspaceKeysPrefix)
			if !isKeys {
				continue
			}
			if n, err := strconv.ParseUint(raw, numericBaseDecimal, redisCounterBitSize); err == nil {
				total += n
			}
		}
	}
	return total
}

// redisRejectedCalls sums rejected_calls — commands refused before they ran
// (OOM under noeviction, LOADING, a wrong arity) — over every command in an
// INFO commandstats section. A server that predates the counter reports none,
// so the field is then missing rather than a misleading 0.
func redisRejectedCalls(fields map[string]string) (uint64, bool) {
	var total uint64
	found := false
	for name, stats := range fields {
		if !strings.HasPrefix(name, redisCommandStatsPrefix) {
			continue
		}
		for stat := range strings.SplitSeq(stats, redisKeyspaceStatsSep) {
			raw, isRejected := strings.CutPrefix(stat, redisRejectedCallsPrefix)
			if !isRejected {
				continue
			}
			if n, err := strconv.ParseUint(raw, numericBaseDecimal, redisCounterBitSize); err == nil {
				total += n
				found = true
			}
		}
	}
	return total, found
}

// redisMaxMemoryUsedPct reports used_memory as a percentage of maxmemory. With
// no limit configured (maxmemory 0) there is nothing to fill, so it reports 0 —
// a catalog threshold then stays quiet instead of failing on a missing field.
func redisMaxMemoryUsedPct(fields map[string]string) (string, bool) {
	limit, err := strconv.ParseFloat(fields[redisInfoMaxMemory], redisPercentFloatBitSize)
	if err != nil || limit < 0 {
		return "", false
	}
	if limit == 0 {
		return redisMaxMemoryUnlimited, true
	}
	used, err := strconv.ParseFloat(fields[redisInfoUsedMemory], redisPercentFloatBitSize)
	if err != nil {
		return "", false
	}
	return strconv.FormatFloat(used*redisPercentScale/limit, 'f', redisPercentDecimals, redisPercentFloatBitSize), true
}
