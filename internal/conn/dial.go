package conn

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"time"
)

// pqBindDialer adapts *net.Dialer to lib/pq's Dialer/DialerContext interfaces.
type pqBindDialer struct {
	*net.Dialer
}

func (d pqBindDialer) Dial(network, address string) (net.Conn, error) {
	return d.Dialer.Dial(network, address)
}

func (d pqBindDialer) DialTimeout(network, address string, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return d.Dialer.DialContext(ctx, network, address)
}

func (d pqBindDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d.Dialer.DialContext(ctx, network, address)
}

func pqDialer(iface string) pqBindDialer {
	return pqBindDialer{BindDialer(iface)}
}

// probeBanner dials cfg (port defaulting to defaultPort), runs handshake on the
// connection and closes it. It folds the dial / defer-close prologue that every
// banner protocol's Probe repeats; the protocol supplies only its default port
// and handshake.
func probeBanner(ctx context.Context, cfg Config, defaultPort int, handshake func(io.ReadWriter, Config) (Result, error)) (Result, error) {
	c, err := dialDeadline(ctx, cfg, defaultPort)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	return handshake(c, cfg)
}

// dialConn opens a TCP connection to host:port, egressing through cfg.Interface
// when set (SO_BINDTODEVICE), and wrapping it in TLS when cfg.TLS is truthy
// (implicit TLS). normalizeTLS interprets cfg.TLS: "" → plaintext, "skip-verify" →
// TLS without certificate verification, anything else → verified TLS. Shared by
// the natively-probed protocols (redis, imap, …).
func dialConn(ctx context.Context, cfg Config, port int) (net.Conn, error) {
	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}
	addr := hostPort(host, port)
	d := BindDialer(cfg.Interface)
	switch normalizeTLS(cfg.TLS) {
	case "":
		return d.DialContext(ctx, networkTCP, addr)
	case tlsSkipVerify:
		tc := tlsClientConfig(host)
		tc.InsecureSkipVerify = true // operator chose tls: skip-verify
		return (&tls.Dialer{NetDialer: d, Config: tc}).DialContext(ctx, networkTCP, addr)
	default:
		return (&tls.Dialer{NetDialer: d, Config: tlsClientConfig(host)}).DialContext(ctx, networkTCP, addr)
	}
}

// tlsClientConfig is the TLS client config the conn probes share for an upgrade
// to host: SNI set and TLS 1.2 the floor. Centralizing it keeps the minimum
// version (and any future policy) in one place across every probe; callers that
// need to skip verification set InsecureSkipVerify on the returned config.
func tlsClientConfig(host string) *tls.Config {
	return &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
}

// applyDeadline sets the context deadline on a connection (net.Conn or
// net.PacketConn — both satisfy the SetDeadline interface) when the context
// carries one. A context without a deadline is a no-op. It centralizes the
// "propagate the probe timeout to the socket" step every protocol repeats.
func applyDeadline(ctx context.Context, c interface{ SetDeadline(time.Time) error }) {
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
}

// dialDeadline opens the connection a probe needs and applies the context
// deadline to it: cfg.Socket selects a Unix socket; otherwise it dials
// host:port (port defaulting to defaultPort, with TLS/interface handled by
// dialConn). The caller closes the returned connection. It folds the
// port-default + dial + deadline prologue shared by the byte-protocol probes
// into one call.
func dialDeadline(ctx context.Context, cfg Config, defaultPort int) (net.Conn, error) {
	var (
		c   net.Conn
		err error
	)
	if cfg.Socket != "" {
		c, err = dialUnix(ctx, cfg.Socket)
	} else {
		port := cfg.Port
		if port == 0 {
			port = defaultPort
		}
		c, err = dialConn(ctx, cfg, port)
	}
	if err != nil {
		return nil, err
	}
	applyDeadline(ctx, c)
	return c, nil
}

// dialUnix dials a Unix-domain socket. It is the one-liner the socket-only
// probes (acpid, fail2ban, lvmpolld, docker, …) and dialDeadline share, so the
// net.Dialer incantation lives in one place.
func dialUnix(ctx context.Context, socket string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, networkUnix, socket)
}

// probeDialer returns a dialer for driver-backed protocol probes. When iface is
// non-empty it egresses through SO_BINDTODEVICE (BindDialer); timeout bounds the
// TCP connect only.
func probeDialer(iface string, timeout time.Duration) *net.Dialer {
	d := BindDialer(iface)
	if timeout > 0 {
		d.Timeout = timeout
	}
	return d
}
