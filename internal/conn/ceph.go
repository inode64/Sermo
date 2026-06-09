package conn

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
)

func init() { Register(cephProtocol{}, "ceph-mon") }

// cephProtocol probes a Ceph monitor over the Ceph messenger protocol. On
// connect a Ceph daemon (mon/osd/mgr) sends a banner: "ceph v2\n" for messenger
// v2 (default port 3300) or "ceph v027" for the legacy messenger v1 (port 6789).
// Reading a "ceph v" banner proves it is a Ceph endpoint and identifies the
// messenger version. No auth (the banner precedes the authenticated handshake).
type cephProtocol struct{}

func (cephProtocol) Name() string       { return "ceph" }
func (cephProtocol) DefaultPort() int   { return 3300 }
func (cephProtocol) RequiresUser() bool { return false }

func (cephProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 3300
	}
	c, err := BindDialer(cfg.Interface).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}

	buf := make([]byte, 8) // "ceph v2\n" / first 8 bytes of "ceph v027"
	if _, err := io.ReadFull(c, buf); err != nil {
		return Result{}, err
	}
	messenger, ok := parseCephBanner(buf)
	if !ok {
		return Result{}, fmt.Errorf("not a Ceph messenger banner: %q", buf)
	}
	return Result{Extra: map[string]string{"messenger": messenger}}, nil
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
