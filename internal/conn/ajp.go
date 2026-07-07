package conn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
)

func init() { Register(ajpProtocol{}) }

// AJP13 ping/pong prefix codes.
const (
	ajpCPing      = 0x0A // web server -> container: are you alive?
	ajpCPong      = 0x09 // container -> web server: yes
	ajpReplyCPong = "cpong"
)

// ajpProtocol probes an Apache JServ Protocol (AJP13) connector — Tomcat's AJP
// port, used by front-ends like Apache/nginx. It sends a CPing and expects a
// CPong, the same liveness probe those front-ends use. No authentication (AJP is
// a trusted-network protocol).
type ajpProtocol struct{}

func (ajpProtocol) Name() string       { return ProtocolNameAJP }
func (ajpProtocol) DefaultPort() int   { return defaultPortAJP }
func (ajpProtocol) RequiresUser() bool { return false }

func (ajpProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}
	port := cfg.Port
	if port == 0 {
		port = defaultPortAJP
	}
	c, err := BindDialer(cfg.Interface).DialContext(ctx, networkTCP, net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	applyDeadline(ctx, c)

	if _, err := c.Write(buildAJPCPing()); err != nil {
		return Result{}, err
	}
	// Read the full reply, not a single Read: TCP may split the small CPong across
	// segments, and a short Read would falsely report a live connector as down.
	header := make([]byte, 4)
	if _, err := io.ReadFull(c, header); err != nil {
		return Result{}, err
	}
	if header[0] != 0x41 || header[1] != 0x42 { // "AB"
		return Result{}, errors.New("not an AJP response (bad magic)")
	}
	length := int(header[2])<<8 | int(header[3])
	if length < 1 || length > 8192 { // AJP packets are bounded at 8KiB
		return Result{}, errors.New("invalid AJP response length")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c, payload); err != nil {
		return Result{}, err
	}
	prefix, err := parseAJPResponse(append(header, payload...))
	if err != nil {
		return Result{}, err
	}
	if !ajpIsCPong(prefix) {
		return Result{}, fmt.Errorf("unexpected AJP reply prefix %#x (want CPong)", prefix)
	}
	return Result{Extra: map[string]string{extraReply: ajpReplyCPong}}, nil
}

// buildAJPCPing builds an AJP13 CPing packet (web-server-to-container magic
// 0x1234, a one-byte payload of the CPing prefix).
func buildAJPCPing() []byte {
	return []byte{0x12, 0x34, 0x00, 0x01, ajpCPing}
}

// parseAJPResponse validates a container-to-web-server packet (magic "AB",
// 2-byte length) and returns its first payload byte (the prefix code).
func parseAJPResponse(b []byte) (prefix byte, err error) {
	if len(b) < 5 {
		return 0, errors.New("short AJP response")
	}
	if b[0] != 0x41 || b[1] != 0x42 { // "AB"
		return 0, errors.New("not an AJP response (bad magic)")
	}
	length := int(b[2])<<8 | int(b[3])
	if length < 1 || len(b) < 4+length {
		return 0, errors.New("truncated AJP response")
	}
	return b[4], nil
}

func ajpIsCPong(prefix byte) bool { return prefix == ajpCPong }
