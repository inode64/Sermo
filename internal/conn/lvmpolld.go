package conn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

func init() { Register(lvmpolldProtocol{}) }

// DefaultLVMPolldSocket is lvmpolld's well-known control socket.
const DefaultLVMPolldSocket = "/run/lvm/lvmpolld.socket"

const (
	lvmDaemonFieldProtocol   = "protocol"
	lvmDaemonFieldResponse   = "response"
	lvmDaemonFieldVersion    = "version"
	lvmDaemonProtocolVersion = "protocol_version"
	lvmDaemonResponseOK      = "OK"
)

// lvmpolldProtocol probes LVM's poll daemon (lvmpolld) over its Unix socket
// using LVM's generic daemon protocol (the "libdaemon" framework). The client
// speaks first: it sends a `hello` request — a config body `request = "hello"`
// framed by a trailing `\n##\n` delimiter — and the daemon replies with the same
// framing carrying `response = "OK"`, `protocol = "lvmpolld"` and a protocol
// `version`. A well-formed `OK` reply proves lvmpolld is up and speaking its
// protocol (a stale socket left by a dead daemon refuses the connection). Result
// data carries the reported `protocol` and `protocol_version`; there is no lvm2
// software version in the handshake, so the result has no version. Socket-only
// (no TCP port), no auth.
type lvmpolldProtocol struct{}

func (lvmpolldProtocol) Name() string       { return ProtocolNameLVMPolld }
func (lvmpolldProtocol) DefaultPort() int   { return defaultPortNone }
func (lvmpolldProtocol) RequiresUser() bool { return false }

func (lvmpolldProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	socket := cfg.Socket
	if socket == "" {
		socket = DefaultLVMPolldSocket
	}
	c, err := dialUnix(ctx, socket)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	applyDeadline(ctx, c)

	// The hello request body is a single config field; buffer framing appends the
	// "\n##\n" delimiter (matching libdaemon's buffer_write exactly).
	if _, err := io.WriteString(c, "request = \"hello\"\n\n##\n"); err != nil {
		return Result{}, err
	}
	reply, err := readLVMDaemonMessage(c)
	if err != nil {
		return Result{}, err
	}
	fields := parseLVMDaemonReply(reply)
	if fields[lvmDaemonFieldResponse] != lvmDaemonResponseOK {
		return Result{}, fmt.Errorf("lvmpolld hello: response = %q", fields[lvmDaemonFieldResponse])
	}
	// Guard against pointing at a different LVM daemon (lvmetad, dmeventd) that
	// shares the protocol but answers a different name.
	if p := fields[lvmDaemonFieldProtocol]; p != "" && p != ProtocolNameLVMPolld {
		return Result{}, fmt.Errorf("lvmpolld hello: protocol = %q, not lvmpolld", p)
	}
	extra := map[string]string{extraSocket: socket}
	if p := fields[lvmDaemonFieldProtocol]; p != "" {
		extra[extraProtocol] = p
	}
	if v := fields[lvmDaemonFieldVersion]; v != "" {
		extra[lvmDaemonProtocolVersion] = v
	}
	return Result{Extra: extra}, nil
}

// readLVMDaemonMessage reads one LVM daemon protocol message: bytes up to the
// "\n##\n" delimiter, which it strips. It caps the message to guard against a
// misbehaving or non-LVM peer that never sends the delimiter.
func readLVMDaemonMessage(r io.Reader) (string, error) {
	const maxLen = 1 << 16
	delim := []byte("\n##\n")
	buf := make([]byte, 0, 256)
	tmp := make([]byte, 512)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if i := bytes.Index(buf, delim); i >= 0 {
				return string(buf[:i]), nil
			}
			if len(buf) > maxLen {
				return "", errors.New("lvmpolld: reply exceeds size limit")
			}
		}
		if err != nil {
			if err == io.EOF && len(buf) > 0 {
				return "", errors.New("lvmpolld: connection closed before message delimiter")
			}
			return "", err
		}
	}
}

// parseLVMDaemonReply parses an LVM daemon config reply into a flat map. Each
// field is a `key = value` line; string values are double-quoted, integers bare.
// The hello reply is flat (response/protocol/version), so nested sections — which
// do not occur here — are simply ignored.
func parseLVMDaemonReply(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.Trim(strings.TrimSpace(line[eq+1:]), "\"")
		if key != "" {
			out[key] = val
		}
	}
	return out
}
