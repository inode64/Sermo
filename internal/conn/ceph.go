package conn

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
)

func init() { Register(cephProtocol{}, protocolAliasCephMon) }

// cephProtocol probes a Ceph monitor over the Ceph messenger protocol. On
// connect a Ceph daemon (mon/osd/mgr) sends a banner: "ceph v2\n" for messenger
// v2 (default port 3300) or "ceph v027" for the legacy messenger v1 (port 6789).
// Reading a "ceph v" banner proves it is a Ceph endpoint and identifies the
// messenger version. No auth (the banner precedes the authenticated handshake).
type cephProtocol struct{}

func (cephProtocol) Name() string       { return ProtocolNameCeph }
func (cephProtocol) DefaultPort() int   { return defaultPortCeph }
func (cephProtocol) RequiresUser() bool { return false }

func (cephProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}
	port := cfg.Port
	if port == 0 {
		port = defaultPortCeph
	}
	c, err := BindDialer(cfg.Interface).DialContext(ctx, networkTCP, net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	applyDeadline(ctx, c)

	buf := make([]byte, 8) // "ceph v2\n" / first 8 bytes of "ceph v027"
	if _, err := io.ReadFull(c, buf); err != nil {
		return Result{}, err
	}
	messenger, ok := parseCephBanner(buf)
	if !ok {
		return Result{}, fmt.Errorf("not a Ceph messenger banner: %q", buf)
	}
	return Result{Extra: map[string]string{extraMessenger: messenger}}, nil
}

// parseCephBanner identifies the Ceph messenger version from the connect banner:
// "ceph v2…" -> "v2", "ceph v0…" (e.g. "ceph v027") -> "v1".
func parseCephBanner(b []byte) (string, bool) {
	if len(b) < 7 || string(b[:6]) != "ceph v" {
		return "", false
	}
	if b[6] == '2' {
		return "v2", true
	}
	return "v1", true
}
