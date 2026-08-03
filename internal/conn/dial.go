package conn

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"time"
)

// pqBindDialer adapts *net.Dialer to lib/pq's Dialer/DialerContext interfaces.
type pqBindDialer struct {
	*net.Dialer
}

func (d pqBindDialer) Dial(network, address string) (net.Conn, error) {
	c, err := d.Dialer.Dial(network, address)
	if err != nil {
		return nil, wrapDialError(network, address, err)
	}
	return c, nil
}

func (d pqBindDialer) DialTimeout(network, address string, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c, err := d.Dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, wrapDialError(network, address, err)
	}
	return c, nil
}

func (d pqBindDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	c, err := d.Dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, wrapDialError(network, address, err)
	}
	return c, nil
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
	host, _ := cfg.hostPortDefaults(port)
	addr := hostPort(host, port)
	d := BindDialer(cfg.Interface)
	var (
		c   net.Conn
		err error
	)
	switch NormalizeTLS(cfg.TLS) {
	case "":
		c, err = d.DialContext(ctx, networkTCP, addr)
	case tlsSkipVerify:
		tc := tlsClientConfig(host)
		tc.InsecureSkipVerify = true // operator chose tls: skip-verify
		c, err = (&tls.Dialer{NetDialer: d, Config: tc}).DialContext(ctx, networkTCP, addr)
	default:
		c, err = (&tls.Dialer{NetDialer: d, Config: tlsClientConfig(host)}).DialContext(ctx, networkTCP, addr)
	}
	if err != nil {
		return nil, wrapDialError(networkTCP, addr, err)
	}
	return c, nil
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
		return nil, fmt.Errorf("open probe connection: %w", err)
	}
	applyDeadline(ctx, c)
	return c, nil
}

// dialUnix dials a Unix-domain socket. It is the one-liner the socket-only
// probes (acpid, fail2ban, lvmpolld, docker, …) and dialDeadline share, so the
// net.Dialer incantation lives in one place.
func dialUnix(ctx context.Context, socket string) (net.Conn, error) {
	c, err := (&net.Dialer{}).DialContext(ctx, networkUnix, socket)
	if err != nil {
		return nil, wrapDialError(networkUnix, socket, err)
	}
	return c, nil
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
	return dialNetworkDeadline(ctx, cfg, defaultPort, networkTCP)
}

// dialNetworkDeadline opens a connection to cfg's defaulted address through
// BindDialer and applies the context deadline. TCP and UDP probes keep their
// named wrappers while sharing the common dial lifecycle here.
func dialNetworkDeadline(ctx context.Context, cfg Config, defaultPort int, network string) (net.Conn, error) {
	addr := cfg.addrDefaults(defaultPort)
	c, err := BindDialer(cfg.Interface).DialContext(ctx, network, addr)
	if err != nil {
		return nil, wrapDialError(network, addr, err)
	}
	applyDeadline(ctx, c)
	return c, nil
}

// readTextGreeting reads a server's 3-digit greeting through net/textproto —
// the handshake prologue ftp, smtp and nntp share. The returned reader carries
// the buffered stream, so the caller must keep using it for later responses.
func readTextGreeting(rw io.ReadWriter) (*textproto.Reader, int, string, error) {
	tp := textproto.NewReader(bufio.NewReader(rw))
	code, greeting, err := tp.ReadResponse(0)
	if err != nil {
		return tp, code, greeting, fmt.Errorf("read text greeting: %w", err)
	}
	return tp, code, greeting, nil
}

// unexpectedGreeting is the shared refusal for a greeting whose status code is
// not one the protocol accepts.
func unexpectedGreeting(code int, greeting string) error {
	return fmt.Errorf("unexpected greeting: %d %s", code, greeting)
}

// step* name every probe exchange reported through probeErr. They live here, in
// one block, so the operator-facing vocabulary can be read and kept consistent
// in a single place: no probe passes a literal. Unprefixed names are the generic
// exchanges any protocol may reuse; a prefixed name is that protocol's own verb,
// method or frame ("GetId" is the D-Bus method, "cping" the AJP one), where the
// wording is fixed by the wire protocol rather than chosen by us.
const (
	stepAuth           = "auth"
	stepBanner         = "banner"
	stepClose          = "close"
	stepConnect        = "connect"
	stepDial           = "dial"
	stepGet            = "get"
	stepInfo           = "info"
	stepInspect        = "inspect"
	stepListen         = "listen"
	stepLogin          = "login"
	stepOpen           = "open"
	stepPing           = "ping"
	stepQuery          = "query"
	stepReply          = "reply"
	stepRequest        = "request"
	stepResolveServer  = "resolve server"
	stepResponse       = "response"
	stepResponseBody   = "response body"
	stepResponseHeader = "response header"
	stepStats          = "stats"
	stepVersion        = "version"

	// Protocol-specific steps: the wire protocol fixes the wording.
	stepAJPCping                   = "cping"
	stepAJPReplyBody               = "reply body"
	stepAJPReplyHeader             = "reply header"
	stepAMQPFrameHeader            = "frame header"
	stepAMQPFramePayload           = "frame payload"
	stepAMQPProtocolHeader         = "protocol header"
	stepAvahiGetVersionString      = "GetVersionString"
	stepChronyClientSocket         = "client socket"
	stepChronyClientSocketMode     = "client socket mode"
	stepDBusGetID                  = "GetId"
	stepDHCPBindSocket             = "bind socket"
	stepDHCPClientMAC              = "client MAC"
	stepDHCPLeaseScan              = "lease scan"
	stepDHCPServerAddress          = "server address"
	stepDHCPServerPort             = "server port"
	stepDNSBuildQuery              = "build query"
	stepDNSLocalRoute              = "local route"
	stepDNSPackQuery               = "pack query"
	stepDNSParseReply              = "parse reply"
	stepDNSReadResolvConf          = "read resolv.conf"
	stepFPMRecordContent           = "record content"
	stepFPMRecordHeader            = "record header"
	stepGuacdSelect                = "select"
	stepGuacdSelectReply           = "select reply"
	stepKafkaAPIVersionsRequest    = "ApiVersions request"
	stepKafkaResponseSize          = "response size"
	stepLVMPolldHello              = "hello"
	stepLVMPolldReadReply          = "read reply"
	stepMQTTConnack                = "CONNACK"
	stepMQTTConnect                = "CONNECT"
	stepMySQLHandshakeHeader       = "handshake header"
	stepMySQLHandshakePayload      = "handshake payload"
	stepNFSRPCFragment             = "RPC fragment"
	stepNFSRPCFragmentHeader       = "RPC fragment header"
	stepNFSRPCRequest              = "RPC request"
	stepOpenVPNControlFrame        = "control frame"
	stepOpenVPNControlPacket       = "control packet"
	stepOpenVPNFrameBody           = "frame body"
	stepOpenVPNFrameLength         = "frame length"
	stepOpenvSwitchDecodeResult    = "decode result"
	stepPOPPass                    = "PASS"
	stepPOPUser                    = "USER"
	stepPrometheusBuildinfoRequest = "buildinfo request"
	stepPrometheusHealthRequest    = "health request"
	stepRDPNegotiationRequest      = "negotiation request"
	stepRDPNegotiationResponse     = "negotiation response"
	stepRedisBulkString            = "bulk string"
	stepSMBClientGUID              = "client GUID"
	stepSMBNegotiateRequest        = "negotiate request"
	stepSMBPreauthSalt             = "preauth salt"
	stepTFTPReadRequest            = "read request"
	stepUDisks2GetNameOwner        = "GetNameOwner"
	stepUDisks2Hello               = "Hello"
	stepUDisks2PeerPing            = "Peer.Ping"
	stepVarnishCLIBanner           = "CLI banner"
	stepVarnishCLIStatus           = "CLI status"
)

// probeErr is the single wording every protocol probe reports a failure with:
// the protocol name, the step that failed, then the cause. proto is the
// probe's ProtocolName* constant and step names the exchange in the operator's
// terms ("dial", "banner", "auth", "CLI status", …). Wrapping through one
// helper is what lets wrapcheck be enabled per protocol file.
func probeErr(proto, step string, err error) error {
	return fmt.Errorf("%s %s: %w", proto, step, err)
}

// sendTextCommand writes command and reads its status reply through tp. It is
// the write-then-read step every textproto handshake (FTP, SMTP, NNTP) repeats
// per command after readTextGreeting; the caller owns which status codes it
// accepts. command must already carry its CRLF terminator.
func sendTextCommand(rw io.Writer, tp *textproto.Reader, command string) (int, string, error) {
	if _, err := io.WriteString(rw, command); err != nil {
		return 0, "", fmt.Errorf("send text command: %w", err)
	}
	code, text, err := tp.ReadResponse(0)
	if err != nil {
		return code, text, fmt.Errorf("read text command reply: %w", err)
	}
	return code, text, nil
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
			return Result{}, fmt.Errorf("write protocol command: %w", err)
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

// dialUDPDeadline opens a connected UDP socket to cfg's host (defaulting to
// DefaultHost) and port (defaulting to defaultPort) through BindDialer and
// applies the context deadline. The UDP twin of dialTCPDeadline; the caller
// closes the connection. Probes that exchange more than one datagram with the
// same peer (chrony) dial once through this instead of repeating exchangeUDP.
func dialUDPDeadline(ctx context.Context, cfg Config, defaultPort int) (net.Conn, error) {
	return dialNetworkDeadline(ctx, cfg, defaultPort, networkUDP)
}

// exchangeUDP dials cfg's host (defaulting to DefaultHost) and port
// (defaulting to defaultPort) over UDP through BindDialer, applies the context
// deadline, sends request, and returns the first reply datagram (up to
// bufBytes). The round-trip shared by the datagram probes (rpcbind, nebula).
func exchangeUDP(ctx context.Context, cfg Config, defaultPort int, request []byte, bufBytes int) ([]byte, error) {
	c, err := dialUDPDeadline(ctx, cfg, defaultPort)
	if err != nil {
		return nil, fmt.Errorf("open UDP exchange: %w", err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Write(request); err != nil {
		return nil, fmt.Errorf("write UDP exchange: %w", err)
	}
	buf := make([]byte, bufBytes)
	n, err := c.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("read UDP exchange: %w", err)
	}
	return buf[:n], nil
}

func wrapDialError(network, address string, err error) error {
	return fmt.Errorf("dial %s %q: %w", network, address, err)
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
