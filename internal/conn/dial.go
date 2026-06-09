package conn

import (
	"context"
	"crypto/tls"
	"net"
	"strconv"
)

// dialConn opens a TCP connection to host:port, wrapping it in TLS when tlsMode
// is truthy (implicit TLS). normalizeTLS interprets tlsMode: "" → plaintext,
// "skip-verify" → TLS without certificate verification, anything else → verified
// TLS. Shared by the natively-probed protocols (redis, imap).
func dialConn(ctx context.Context, host string, port int, tlsMode string) (net.Conn, error) {
	if host == "" {
		host = "127.0.0.1"
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	switch normalizeTLS(tlsMode) {
	case "":
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	case "skip-verify":
		cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: true} //nolint:gosec // operator chose tls: skip-verify
		return (&tls.Dialer{Config: cfg}).DialContext(ctx, "tcp", addr)
	default:
		cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
		return (&tls.Dialer{Config: cfg}).DialContext(ctx, "tcp", addr)
	}
}
