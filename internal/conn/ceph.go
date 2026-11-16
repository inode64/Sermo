package conn

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// cephProtocol probes a Ceph monitor over the Ceph messenger protocol. On
// connect a Ceph daemon (mon/osd/mgr) sends a banner: "ceph v2\n" for messenger
// v2 (default port 3300) or "ceph v027" for the legacy messenger v1 (port 6789).
// Reading a "ceph v" banner proves it is a Ceph endpoint and identifies the
// messenger version. No auth (the banner precedes the authenticated handshake).
//
// With a socket it asks one daemon over its admin socket instead, the path
// `ceph --admin-daemon` uses: a JSON command, a length-prefixed JSON reply.
type cephProtocol struct{}

func (cephProtocol) Name() string       { return ProtocolNameCeph }
func (cephProtocol) DefaultPort() int   { return defaultPortCeph }
func (cephProtocol) RequiresUser() bool { return false }

const (
	cephBannerBytes            = 8
	cephBannerMinBytes         = 7
	cephBannerPrefix           = "ceph v"
	cephMessengerVersionOffset = 6
	cephMessengerV1            = "v1"
	cephMessengerV2            = "v2"
	cephMessengerV2Byte        = '2'
)

const (
	// cephAdminDefaultCommand is the one admin command every Ceph daemon type
	// answers; a monitor reports its quorum role under mon_status.
	cephAdminDefaultCommand = "status"
	cephAdminCommandPrefix  = "prefix"
	cephAdminLengthBytes    = 4
	// A monitor's mon_status carries the whole monmap; 4 MiB bounds a peer that
	// announces a length it will never send.
	cephAdminMaxReplyBytes = 4 << 20
	cephAdminStepReply     = "admin reply"
)

func (cephProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	if cfg.Socket != "" {
		return probeCephAdminSocket(ctx, cfg)
	}
	c, err := newProbeTarget(cfg, defaultPortCeph).openTCP(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()

	buf := make([]byte, cephBannerBytes)
	if _, err := io.ReadFull(c, buf); err != nil {
		return Result{}, probeErr(ProtocolNameCeph, stepBanner, err)
	}
	messenger, ok := parseCephBanner(buf)
	if !ok {
		return Result{}, fmt.Errorf("not a Ceph messenger banner: %q", buf)
	}
	return Result{Extra: map[string]string{extraMessenger: messenger}}, nil
}

// probeCephAdminSocket sends one read-only admin command (cfg.Query, default
// "status") as a NUL-terminated JSON object and reads the reply: a big-endian
// 32-bit length, then that many bytes. A daemon answers a known command with a
// JSON object and an unknown one with plain "ERROR: …" text, so a decoded
// object proves the daemon is alive and serving its admin interface.
func probeCephAdminSocket(ctx context.Context, cfg Config) (Result, error) {
	command := cfg.Query
	if command == "" {
		command = cephAdminDefaultCommand
	}
	request, err := json.Marshal(map[string]string{cephAdminCommandPrefix: command})
	if err != nil {
		return Result{}, fmt.Errorf("ceph admin command %q: %w", command, err)
	}
	c, err := newProbeTarget(cfg, defaultPortNone).openStream(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Write(append(request, 0)); err != nil {
		return Result{}, probeErr(ProtocolNameCeph, stepRequest, err)
	}
	var header [cephAdminLengthBytes]byte
	if _, err := io.ReadFull(c, header[:]); err != nil {
		return Result{}, probeErr(ProtocolNameCeph, cephAdminStepReply, err)
	}
	length := binary.BigEndian.Uint32(header[:])
	if length > cephAdminMaxReplyBytes {
		return Result{}, fmt.Errorf("ceph admin reply of %d bytes exceeds the %d byte limit", length, cephAdminMaxReplyBytes)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(c, body); err != nil {
		return Result{}, probeErr(ProtocolNameCeph, cephAdminStepReply, err)
	}
	var reply map[string]any
	if err := json.Unmarshal(body, &reply); err != nil {
		return Result{}, fmt.Errorf("ceph admin command %q: %s", command, firstReplyLine(body))
	}
	extra := map[string]string{extraSocket: cfg.Socket, extraQuery: command}
	if state, ok := reply[ExtraKeyState].(string); ok && state != "" {
		extra[ExtraKeyState] = state
	}
	return Result{Extra: extra}, nil
}

// firstReplyLine quotes what a daemon said instead of JSON, without its tail.
func firstReplyLine(body []byte) string {
	line, _, _ := bytes.Cut(bytes.TrimSpace(body), []byte("\n"))
	if len(line) == 0 {
		return "empty reply"
	}
	return string(line)
}

// parseCephBanner identifies the Ceph messenger version from the connect banner:
// "ceph v2…" -> "v2", "ceph v0…" (e.g. "ceph v027") -> "v1".
func parseCephBanner(b []byte) (string, bool) {
	if len(b) < cephBannerMinBytes || string(b[:len(cephBannerPrefix)]) != cephBannerPrefix {
		return "", false
	}
	if b[cephMessengerVersionOffset] == cephMessengerV2Byte {
		return cephMessengerV2, true
	}
	return cephMessengerV1, true
}
