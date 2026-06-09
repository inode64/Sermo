package conn

import (
	"context"
	"crypto/tls"
	"net"
	"strconv"
)

// dialConn opens a TCP connection to host:port, egressing through cfg.Interface
// when set (SO_BINDTODEVICE), and wrapping it in TLS when cfg.TLS is truthy
// (implicit TLS). normalizeTLS interprets cfg.TLS: "" → plaintext, "skip-verify" →
// TLS without certificate verification, anything else → verified TLS. Shared by
// the natively-probed protocols (redis, imap, …).
func dialConn(ctx context.Context, cfg Config, port int) (net.Conn, error) {
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	d := BindDialer(cfg.Interface)
	switch normalizeTLS(cfg.TLS) {
	case "":
		return d.DialContext(ctx, "tcp", addr)
	case "skip-verify":
		tc := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: true} //nolint:gosec // operator chose tls: skip-verify
		return (&tls.Dialer{NetDialer: d, Config: tc}).DialContext(ctx, "tcp", addr)
	default:
		tc := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
		return (&tls.Dialer{NetDialer: d, Config: tc}).DialContext(ctx, "tcp", addr)
	}
}
