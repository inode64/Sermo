package conn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
)

func init() { Register(ajpProtocol{}) }

// AJP13 ping/pong prefix codes.
const (
	ajpCPing = 0x0A // web server -> container: are you alive?
	ajpCPong = 0x09 // container -> web server: yes
)

// ajpProtocol probes an Apache JServ Protocol (AJP13) connector — Tomcat's AJP
// port, used by front-ends like Apache/nginx. It sends a CPing and expects a
// CPong, the same liveness probe those front-ends use. No authentication (AJP is
// a trusted-network protocol).
type ajpProtocol struct{}

func (ajpProtocol) Name() string       { return "ajp" }
func (ajpProtocol) DefaultPort() int   { return 8009 }
func (ajpProtocol) RequiresUser() bool { return false }

func (ajpProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 8009
	}
	c, err := BindDialer(cfg.Interface).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}

	if _, err := c.Write(buildAJPCPing()); err != nil {
		return Result{}, err
	}
	buf := make([]byte, 16)
	n, err := c.Read(buf)
	if err != nil {
		return Result{}, err
	}
	prefix, err := parseAJPResponse(buf[:n])
	if err != nil {
		return Result{}, err
	}
	if !ajpIsCPong(prefix) {
		return Result{}, fmt.Errorf("unexpected AJP reply prefix %#x (want CPong)", prefix)
	}
	return Result{Extra: map[string]string{"reply": "cpong"}}, nil
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
