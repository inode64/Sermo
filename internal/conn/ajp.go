package conn

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// AJP13 ping/pong prefix codes.
const (
	ajpCPing      = 0x0A // web server -> container: are you alive?
	ajpCPong      = 0x09 // container -> web server: yes
	ajpReplyCPong = "cpong"
)

const (
	ajpHeaderBytes            = 4
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
	c, err := newProbeTarget(cfg, defaultPortAJP).openTCP(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()

	if _, err := c.Write(buildAJPCPing()); err != nil {
		return Result{}, probeErr(ProtocolNameAJP, stepAJPCping, err)
	}
	prefix, err := parseAJPResponse(c)
	if err != nil {
		return Result{}, err
	}
	if prefix != ajpCPong {
		return Result{}, fmt.Errorf("unexpected AJP reply prefix %#x (want CPong)", prefix)
	}
	return Result{Extra: map[string]string{extraReply: ajpReplyCPong}}, nil
}

// buildAJPCPing builds an AJP13 CPing packet (web-server-to-container magic
// 0x1234, a one-byte payload of the CPing prefix).
func buildAJPCPing() []byte {
	return []byte{ajpMagicRequestHigh, ajpMagicRequestLow, ajpCPingPayloadLengthHigh, ajpCPingPayloadLength, ajpCPing}
}

// parseAJPResponse reads and validates one bounded container-to-web-server
// packet. ReadFull preserves correctness when TCP splits even a small CPong.
func parseAJPResponse(r io.Reader) (byte, error) {
	var header [ajpHeaderBytes]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, probeErr(ProtocolNameAJP, stepAJPReplyHeader, err)
	}
	if header[ajpMagicHighOffset] != ajpMagicResponseHigh || header[ajpMagicLowOffset] != ajpMagicResponseLow {
		return 0, errors.New("not an AJP response (bad magic)")
	}
	length := int(header[ajpLengthHighOffset])<<ajpLengthShift | int(header[ajpLengthLowOffset])
	if length < ajpCPingPayloadLength || length > ajpMaxPacketBytes {
		return 0, errors.New("invalid AJP response length")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, probeErr(ProtocolNameAJP, stepAJPReplyBody, err)
	}
	return payload[0], nil
}
