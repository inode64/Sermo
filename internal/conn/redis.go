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
	port := cfg.Port
	if port == 0 {
		port = 6379
	}
	c, err := dialConn(ctx, cfg.Host, port, cfg.TLS)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
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

	// Version: best effort; a successful PING already proves connect + auth.
	version := ""
	if writeRESP(rw, "INFO", "server") == nil {
		if info, err := readRESP(br); err == nil {
			version = redisVersion(info)
		}
	}
	return Result{Version: version}, nil
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

// redisVersion extracts the redis_version value from an INFO reply.
func redisVersion(info string) string {
	for _, line := range strings.Split(info, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "redis_version:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
