package conn

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
)

func init() { Register(rdpProtocol{}, protocolAliasMSWBTServer) }

// RDP negotiation requested protocols (MS-RDPBCGR): standard RDP security, TLS
// and CredSSP/NLA — advertising all lets the server pick and report its policy.
const rdpRequestedProtocols = 0x00000003 // PROTOCOL_SSL | PROTOCOL_HYBRID

const (
	rdpNegotiationFailure      = "negotiation-failure"
	rdpProtocolHybrid          = "hybrid"
	rdpProtocolHybridEx        = "hybrid-ex"
	rdpProtocolTLS             = "tls"
	rdpReadBufferBytes         = 512
	rdpSecurityCredSSPHybrid   = 2
	rdpSecurityCredSSPHybridEx = 8
	rdpSecurityStandard        = 0
	rdpSecurityTLS             = 1
)

const (
	rdpMinConfirmBytes          = 11
	rdpNegFailureType           = 0x03
	rdpNegPacketTypeOffset      = 0
	rdpNegRequestBytes          = 8
	rdpNegRequestType           = 0x01
	rdpNegResponseProtocolEnd   = 19
	rdpNegResponseProtocolStart = 15
	rdpNegResponseType          = 0x02
	rdpNegResponseTypeOffset    = 11
	rdpProtocolLengthOffset     = 2
	rdpProtocolListOffset       = 4
)

const (
	tpktHeaderBytes            = 4
	tpktLengthOffset           = 2
	tpktVersion                = 0x03
	tpktVersionOffset          = 0
	x224ClassByte              = 0
	x224ConnectionConfirm      = 0xD0
	x224ConnectionRequest      = 0xE0
	x224HeaderBytes            = 7
	x224LengthIndicatorOffset  = 0
	x224PDUTypeMask            = 0xF0
	x224PDUTypeOffset          = 5
	x224RequestPDUTypeOffset   = 1
	x224RequestVariableLenBase = 6
)

// rdpProtocol probes a Remote Desktop server natively (MS-RDPBCGR): it sends an
// X.224 Connection Request carrying an RDP Negotiation Request and verifies the
// server answers with an X.224 Connection Confirm — proof it is up and speaking
// RDP. The negotiated security protocol (standard RDP / TLS / CredSSP-NLA) is
// reported. No auth (the negotiation precedes authentication).
type rdpProtocol struct{}

func (rdpProtocol) Name() string       { return ProtocolNameRDP }
func (rdpProtocol) DefaultPort() int   { return defaultPortRDP }
func (rdpProtocol) RequiresUser() bool { return false }

func (rdpProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}
	port := cfg.Port
	if port == 0 {
		port = defaultPortRDP
	}
	c, err := BindDialer(cfg.Interface).DialContext(ctx, networkTCP, hostPort(host, port))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	applyDeadline(ctx, c)

	if _, err := c.Write(buildRDPNegRequest(rdpRequestedProtocols)); err != nil {
		return Result{}, err
	}
	buf := make([]byte, rdpReadBufferBytes)
	n, err := c.Read(buf)
	if err != nil {
		return Result{}, err
	}
	security, err := parseRDPConfirm(buf[:n])
	if err != nil {
		return Result{}, err
	}
	return Result{Extra: map[string]string{extraSecurity: security}}, nil
}

// buildRDPNegRequest builds a TPKT + X.224 Connection Request enclosing an RDP
// Negotiation Request that advertises protocols.
func buildRDPNegRequest(protocols uint32) []byte {
	neg := make([]byte, rdpNegRequestBytes)
	neg[rdpNegPacketTypeOffset] = rdpNegRequestType
	binary.LittleEndian.PutUint16(neg[rdpProtocolLengthOffset:], rdpNegRequestBytes)
	binary.LittleEndian.PutUint32(neg[rdpProtocolListOffset:], protocols)

	// X.224 Connection Request: LI, CR(0xE0), DST-REF, SRC-REF, class.
	x224 := make([]byte, x224HeaderBytes)
	x224[x224LengthIndicatorOffset] = byte(x224RequestVariableLenBase + len(neg))
	x224[x224RequestPDUTypeOffset] = x224ConnectionRequest
	x224[len(x224)-1] = x224ClassByte
	body := append(x224, neg...)

	// TPKT header (version 3).
	pkt := make([]byte, tpktHeaderBytes)
	pkt[tpktVersionOffset] = tpktVersion
	binary.BigEndian.PutUint16(pkt[tpktLengthOffset:], uint16(tpktHeaderBytes+len(body)))
	return append(pkt, body...)
}

// parseRDPConfirm validates a TPKT + X.224 Connection Confirm and returns the
// negotiated security protocol. Any valid Connection Confirm proves an RDP
// server; a negotiation failure still counts as up (it answered).
func parseRDPConfirm(b []byte) (string, error) {
	if len(b) < rdpMinConfirmBytes {
		return "", errors.New("short RDP response")
	}
	if b[tpktVersionOffset] != tpktVersion {
		return "", fmt.Errorf("not a TPKT response (0x%02x)", b[tpktVersionOffset])
	}
	if b[x224PDUTypeOffset]&x224PDUTypeMask != x224ConnectionConfirm {
		return "", fmt.Errorf("not an X.224 Connection Confirm (0x%02x)", b[x224PDUTypeOffset])
	}
	if len(b) >= rdpNegResponseProtocolEnd {
		switch b[rdpNegResponseTypeOffset] {
		case rdpNegResponseType:
			return rdpProtocolName(binary.LittleEndian.Uint32(b[rdpNegResponseProtocolStart:rdpNegResponseProtocolEnd])), nil
		case rdpNegFailureType:
			return rdpNegotiationFailure, nil
		}
	}
	return ProtocolNameRDP, nil // CC with no negotiation response: standard RDP security
}

func rdpProtocolName(p uint32) string {
	switch p {
	case rdpSecurityStandard:
		return ProtocolNameRDP
	case rdpSecurityTLS:
		return rdpProtocolTLS
	case rdpSecurityCredSSPHybrid:
		return rdpProtocolHybrid
	case rdpSecurityCredSSPHybridEx:
		return rdpProtocolHybridEx
	default:
		return fmt.Sprintf("0x%08x", p)
	}
}
