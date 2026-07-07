package conn

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
)

func init() { Register(rdpProtocol{}, "ms-wbt-server") }

// RDP negotiation requested protocols (MS-RDPBCGR): standard RDP security, TLS
// and CredSSP/NLA — advertising all lets the server pick and report its policy.
const rdpRequestedProtocols = 0x00000003 // PROTOCOL_SSL | PROTOCOL_HYBRID

// rdpProtocol probes a Remote Desktop server natively (MS-RDPBCGR): it sends an
// X.224 Connection Request carrying an RDP Negotiation Request and verifies the
// server answers with an X.224 Connection Confirm — proof it is up and speaking
// RDP. The negotiated security protocol (standard RDP / TLS / CredSSP-NLA) is
// reported. No auth (the negotiation precedes authentication).
type rdpProtocol struct{}

func (rdpProtocol) Name() string       { return "rdp" }
func (rdpProtocol) DefaultPort() int   { return 3389 }
func (rdpProtocol) RequiresUser() bool { return false }

func (rdpProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}
	port := cfg.Port
	if port == 0 {
		port = 3389
	}
	c, err := BindDialer(cfg.Interface).DialContext(ctx, networkTCP, net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	applyDeadline(ctx, c)

	if _, err := c.Write(buildRDPNegRequest(rdpRequestedProtocols)); err != nil {
		return Result{}, err
	}
	buf := make([]byte, 512)
	n, err := c.Read(buf)
	if err != nil {
		return Result{}, err
	}
	security, err := parseRDPConfirm(buf[:n])
	if err != nil {
		return Result{}, err
	}
	return Result{Extra: map[string]string{"security": security}}, nil
}

// buildRDPNegRequest builds a TPKT + X.224 Connection Request enclosing an RDP
// Negotiation Request that advertises protocols.
func buildRDPNegRequest(protocols uint32) []byte {
	neg := make([]byte, 8)
	neg[0] = 0x01 // TYPE_RDP_NEG_REQ
	binary.LittleEndian.PutUint16(neg[2:], 8)
	binary.LittleEndian.PutUint32(neg[4:], protocols)

	// X.224 Connection Request: LI, CR(0xE0), DST-REF, SRC-REF, class.
	x224 := make([]byte, 7)
	x224[0] = byte(6 + len(neg)) // length indicator (header bytes after this octet)
	x224[1] = 0xE0
	body := append(x224, neg...)

	// TPKT header (version 3).
	pkt := make([]byte, 4)
	pkt[0] = 0x03
	binary.BigEndian.PutUint16(pkt[2:], uint16(4+len(body)))
	return append(pkt, body...)
}

// parseRDPConfirm validates a TPKT + X.224 Connection Confirm and returns the
// negotiated security protocol. Any valid Connection Confirm proves an RDP
// server; a negotiation failure still counts as up (it answered).
func parseRDPConfirm(b []byte) (string, error) {
	if len(b) < 11 {
		return "", errors.New("short RDP response")
	}
	if b[0] != 0x03 {
		return "", fmt.Errorf("not a TPKT response (0x%02x)", b[0])
	}
	if b[5]&0xF0 != 0xD0 { // X.224 Connection Confirm (CC)
		return "", fmt.Errorf("not an X.224 Connection Confirm (0x%02x)", b[5])
	}
	if len(b) >= 19 {
		switch b[11] {
		case 0x02: // TYPE_RDP_NEG_RSP
			return rdpProtocolName(binary.LittleEndian.Uint32(b[15:19])), nil
		case 0x03: // TYPE_RDP_NEG_FAILURE
			return "negotiation-failure", nil
		}
	}
	return "rdp", nil // CC with no negotiation response: standard RDP security
}

func rdpProtocolName(p uint32) string {
	switch p {
	case 0:
		return "rdp"
	case 1:
		return "tls"
	case 2:
		return "hybrid" // CredSSP / NLA
	case 8:
		return "hybrid-ex"
	default:
		return fmt.Sprintf("0x%08x", p)
	}
}
