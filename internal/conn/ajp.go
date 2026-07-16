package conn

import (
	"context"
	"errors"
	"fmt"
	"io"
)

func init() { Register(ajpProtocol{}) }

// AJP13 ping/pong prefix codes.
const (
	ajpCPing      = 0x0A // web server -> container: are you alive?
	ajpCPong      = 0x09 // container -> web server: yes
	ajpReplyCPong = "cpong"
)

const (
	ajpHeaderBytes            = 4
	ajpMinResponseBytes       = 5
	ajpMaxPacketBytes         = 8192
	ajpMagicRequestHigh       = 0x12
	ajpMagicRequestLow        = 0x34
	ajpMagicResponseHigh      = 0x41
	ajpMagicResponseLow       = 0x42
	ajpMagicHighOffset        = 0
	ajpMagicLowOffset         = 1
	ajpLengthHighOffset       = 2
	ajpLengthLowOffset        = 3
	ajpLengthShift            = 8
	ajpPayloadOffset          = 4
	ajpCPingPayloadLengthHigh = 0
	ajpCPingPayloadLength     = 1
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
	c, err := dialTCPDeadline(ctx, cfg, defaultPortAJP)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()

	if _, err := c.Write(buildAJPCPing()); err != nil {
		return Result{}, err
	}
	// Read the full reply, not a single Read: TCP may split the small CPong across
	// segments, and a short Read would falsely report a live connector as down.
	var header [ajpHeaderBytes]byte
	if _, err := io.ReadFull(c, header[:]); err != nil {
		return Result{}, err
	}
	if header[ajpMagicHighOffset] != ajpMagicResponseHigh || header[ajpMagicLowOffset] != ajpMagicResponseLow {
		return Result{}, errors.New("not an AJP response (bad magic)")
	}
	length := int(header[ajpLengthHighOffset])<<ajpLengthShift | int(header[ajpLengthLowOffset])
	if length < ajpCPingPayloadLength || length > ajpMaxPacketBytes {
		return Result{}, errors.New("invalid AJP response length")
	}
	packet := make([]byte, ajpHeaderBytes+length)
	copy(packet, header[:])
	if _, err := io.ReadFull(c, packet[ajpPayloadOffset:]); err != nil {
		return Result{}, err
	}
	prefix, err := parseAJPResponse(packet)
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
	return []byte{ajpMagicRequestHigh, ajpMagicRequestLow, ajpCPingPayloadLengthHigh, ajpCPingPayloadLength, ajpCPing}
}

// parseAJPResponse validates a container-to-web-server packet (magic "AB",
// 2-byte length) and returns its first payload byte (the prefix code).
func parseAJPResponse(b []byte) (prefix byte, err error) {
	if len(b) < ajpMinResponseBytes {
		return 0, errors.New("short AJP response")
	}
	if b[ajpMagicHighOffset] != ajpMagicResponseHigh || b[ajpMagicLowOffset] != ajpMagicResponseLow {
		return 0, errors.New("not an AJP response (bad magic)")
	}
	length := int(b[ajpLengthHighOffset])<<ajpLengthShift | int(b[ajpLengthLowOffset])
	if length < ajpCPingPayloadLength || len(b) < ajpHeaderBytes+length {
		return 0, errors.New("truncated AJP response")
	}
	return b[ajpPayloadOffset], nil
}

func ajpIsCPong(prefix byte) bool { return prefix == ajpCPong }
