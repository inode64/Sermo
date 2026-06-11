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
	Register(redisProtocol{}, "valkey")
}

// redisProtocol probes a Redis (or Valkey) server natively over RESP — no
// external driver: the handshake (optional AUTH, then PING, then INFO for the
// version) is a few simple commands, so per the native-Go-first convention it is
// implemented directly on a socket.
type redisProtocol struct{}

func (redisProtocol) Name() string       { return "redis" }
func (redisProtocol) DefaultPort() int   { return 6379 }
func (redisProtocol) RequiresUser() bool { return false }

// Probe connects (over TLS when configured), authenticates if a password/user is
// set, verifies the server answers PING, and reads its version. The caller's
// context bounds the probe.
func (redisProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	c, err := dialDeadline(ctx, cfg, 6379)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	return redisHandshake(c, cfg)
}

// redisHandshake runs the RESP handshake on rw: optional AUTH, a PING that must
// answer PONG, then a best-effort INFO for the server version.
func redisHandshake(rw io.ReadWriter, cfg Config) (Result, error) {
	br := bufio.NewReader(rw)

	if cfg.Password != "" || cfg.User != "" {
		args := []string{"AUTH"}
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

	if err := writeRESP(rw, "PING"); err != nil {
		return Result{}, err
	}
	pong, err := readRESP(br)
	if err != nil {
		return Result{}, err
	}
	if !strings.EqualFold(pong, "PONG") {
		return Result{}, fmt.Errorf("unexpected PING reply %q", pong)
	}

	// Server identity and health: best effort; a successful PING already proves
	// connect + auth. A single INFO carries version plus role, replication,
	// persistence and memory fields, each exposed in Extra so an expect: rule can
	// assert on it (e.g. role == master, master_link_status == up,
	// rdb_last_bgsave_status == ok).
	res := Result{Extra: map[string]string{}}
	if writeRESP(rw, "INFO") == nil {
		if info, err := readRESP(br); err == nil {
			fields := parseRedisInfo(info)
			res.Version = fields["redis_version"]
			for _, k := range []string{
				"role", "master_link_status", "connected_clients",
				"used_memory", "maxmemory", "mem_fragmentation_ratio",
				"rdb_last_bgsave_status", "aof_last_write_status", "loading",
			} {
				if v := fields[k]; v != "" {
					res.Extra[k] = v
				}
			}
			if v := fields["uptime_in_seconds"]; v != "" {
				res.Extra["uptime_seconds"] = v
			}
		}
	}
	return res, nil
}

// writeRESP encodes args as a RESP array of bulk strings.
func writeRESP(w io.Writer, args ...string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// readRESP reads one reply: simple string (+), integer (:) and bulk string ($)
// return their payload; an error reply (-) returns it as a Go error.
func readRESP(br *bufio.Reader) (string, error) {
	line, err := readRESPLine(br)
	if err != nil {
		return "", err
	}
	if line == "" {
		return "", errors.New("empty reply")
	}
	switch line[0] {
	case '+', ':':
		return line[1:], nil
	case '-':
		return "", errors.New(line[1:])
	case '$':
		n, err := strconv.Atoi(line[1:])
		if err != nil {
			return "", fmt.Errorf("bad bulk length %q", line)
		}
		if n < 0 {
			return "", nil // null bulk
		}
		buf := make([]byte, n+2) // payload + trailing CRLF
		if _, err := io.ReadFull(br, buf); err != nil {
			return "", err
		}
		return string(buf[:n]), nil
	default:
		return line[1:], nil
	}
}

func readRESPLine(br *bufio.Reader) (string, error) {
	s, err := br.ReadString('\n')
	return strings.TrimRight(s, "\r\n"), err
}

// parseRedisInfo parses an INFO reply — "key:value" lines, "# Section" headers
// and blank separators — into a flat map. Section headers and blanks are
// dropped; each field is split on its first ':'.
func parseRedisInfo(info string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			out[k] = v
		}
	}
	return out
}
