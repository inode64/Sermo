package conn

import (
	"context"
	"crypto/tls"
	"fmt"
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
	switch NormalizeTLS(cfg.TLS) {
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
func applyDeadline(ctx context.Context, c interface {
	SetDeadline(deadline time.Time) error
}) {
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

// probeUnixSocket verifies that a socket-only daemon is listening. A successful
// connection proves liveness; socket-only protocols that have no safe request
// or reply exchange can use this without blocking for daemon activity.
func probeUnixSocket(ctx context.Context, cfg Config, defaultSocket string) (Result, error) {
	socket := cfg.Socket
	if socket == "" {
		socket = defaultSocket
	}
	c, err := dialUnix(ctx, socket)
	if err != nil {
		return Result{}, err
	}
	_ = c.Close()
	return Result{Extra: map[string]string{extraSocket: socket}}, nil
}

// dialTCPDeadline opens a plain TCP connection to cfg's host (defaulting to
// DefaultHost) and port (defaulting to defaultPort) through BindDialer and
// applies the context deadline. The prologue shared by the byte-protocol
// probes that never upgrade to TLS; the caller closes the connection.
func dialTCPDeadline(ctx context.Context, cfg Config, defaultPort int) (net.Conn, error) {
	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}
	port := cfg.Port
	if port == 0 {
		port = defaultPort
	}
	c, err := BindDialer(cfg.Interface).DialContext(ctx, networkTCP, hostPort(host, port))
	if err != nil {
		return nil, err
	}
	applyDeadline(ctx, c)
	return c, nil
}

// probeLineCommand dials cfg (dialDeadline semantics), optionally sends
// command, reads one greeting line and parses it with parse; a foreign reply
// (parse ok=false) fails with errFormat applied to the offending line. The
// command→greeting skeleton shared by clamd, spamd and asterisk.
func probeLineCommand(ctx context.Context, cfg Config, defaultPort int, command string, parse func(line string) (Result, bool), errFormat string) (Result, error) {
	c, err := dialDeadline(ctx, cfg, defaultPort)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	if command != "" {
		if _, err := io.WriteString(c, command); err != nil {
			return Result{}, err
		}
	}
	line, err := readGreetingLine(c)
	if err != nil {
		return Result{}, err
	}
	res, ok := parse(line)
	if !ok {
		return Result{}, fmt.Errorf(errFormat, line)
	}
	return res, nil
}

// exchangeUDP dials cfg's host (defaulting to DefaultHost) and port
// (defaulting to defaultPort) over UDP through BindDialer, applies the context
// deadline, sends request, and returns the first reply datagram (up to
// bufBytes). The round-trip shared by the datagram probes (rpcbind, nebula).
func exchangeUDP(ctx context.Context, cfg Config, defaultPort int, request []byte, bufBytes int) ([]byte, error) {
	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}
	port := cfg.Port
	if port == 0 {
		port = defaultPort
	}
	c, err := BindDialer(cfg.Interface).DialContext(ctx, networkUDP, hostPort(host, port))
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	applyDeadline(ctx, c)
	if _, err := c.Write(request); err != nil {
		return nil, err
	}
	buf := make([]byte, bufBytes)
	n, err := c.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// socketOnlyProtocol is a Unix-socket-only liveness protocol whose probe is
// the connect itself. Daemons with no safe request/reply exchange (acpid,
// fail2ban) register instances with their well-known socket; the per-daemon
// rationale lives at each registration site.
type socketOnlyProtocol struct {
	name   string
	socket string
}

func (p socketOnlyProtocol) Name() string     { return p.name }
func (socketOnlyProtocol) DefaultPort() int   { return defaultPortNone }
func (socketOnlyProtocol) RequiresUser() bool { return false }

func (p socketOnlyProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	return probeUnixSocket(ctx, cfg, p.socket)
}

// codeName returns the protocol-specific name for code from names, falling
// back to fmt.Sprintf(fallbackFormat, code) for unknown codes.
func codeName[C comparable](code C, names map[C]string, fallbackFormat string) string {
	if name, ok := names[code]; ok {
		return name
	}
	return fmt.Sprintf(fallbackFormat, code)
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
