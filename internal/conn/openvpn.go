package conn

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
)

func init() { Register(openvpnProtocol{}, "ovpn") }

// OpenVPN control-channel opcodes (src/openvpn/ssl_pkt.h). The first byte packs
// the opcode in the high 5 bits and the key id in the low 3 (opcode = b>>3).
const (
	openvpnHardResetClientV2 = 7 // P_CONTROL_HARD_RESET_CLIENT_V2
	openvpnHardResetServerV2 = 8 // P_CONTROL_HARD_RESET_SERVER_V2
)

// openvpnProtocol probes an OpenVPN server natively over its control channel.
// The first step of the OpenVPN handshake is unauthenticated (the TLS exchange
// comes after): a client sends a P_CONTROL_HARD_RESET_CLIENT_V2 and the server
// replies with a P_CONTROL_HARD_RESET_SERVER_V2 that acknowledges the client's
// session id. So the probe sends a hard-reset-client packet carrying a random
// session id and verifies the reply is a hard-reset-server echoing that id —
// proof the server is up and speaking OpenVPN, with no credentials. Default port
// 1194; transport defaults to UDP, `transport: tcp` selects TCP (2-byte
// length-prefixed framing). No auth.
//
// The reset only elicits a reply on a server WITHOUT `tls-auth`/`tls-crypt`:
// those wrap control packets with an HMAC (or encrypt them), so a bare reset is
// dropped without a reply. Against such a server, silence is expected and does
// not prove it is down.
type openvpnProtocol struct{}

func (openvpnProtocol) Name() string       { return "openvpn" }
func (openvpnProtocol) DefaultPort() int   { return 1194 }
func (openvpnProtocol) RequiresUser() bool { return false }

func (openvpnProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 1194
	}
	transport := networkUDP
	if cfg.Params["transport"] == networkTCP {
		transport = networkTCP
	}

	sid := openvpnSessionID()
	c, err := BindDialer(cfg.Interface).DialContext(ctx, transport, net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	applyDeadline(ctx, c)

	reply, err := openvpnExchange(c, transport, openvpnClientReset(sid))
	if err != nil {
		return Result{}, err
	}
	if err := parseOpenVPNReset(reply, sid); err != nil {
		return Result{}, err
	}
	return Result{Extra: map[string]string{"transport": transport, "reply": "hard_reset_server"}}, nil
}

// openvpnSessionID returns a random 8-byte OpenVPN session id.
func openvpnSessionID() []byte {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		copy(b, "OPENVPN_") // 8-byte deterministic fallback
	}
	return b
}

// openvpnClientReset builds the 14-byte first P_CONTROL_HARD_RESET_CLIENT_V2
// packet (key id 0): the opcode byte, the 8-byte session id, an empty ACK array
// (length 0) and message packet-id 0. No tls-auth HMAC is included.
func openvpnClientReset(sid []byte) []byte {
	b := make([]byte, 0, 14)
	b = append(b, openvpnHardResetClientV2<<3) // opcode<<3 | key_id(0)
	b = append(b, sid...)                      // own session id (8 bytes)
	b = append(b, 0x00)                        // ACK array length = 0
	b = append(b, 0, 0, 0, 0)                  // message packet-id = 0
	return b
}

// openvpnExchange sends packet and reads one reply, applying TCP's 2-byte
// big-endian length framing when transport is "tcp" (UDP is unframed).
func openvpnExchange(c net.Conn, transport string, packet []byte) ([]byte, error) {
	if transport == networkTCP {
		frame := make([]byte, 2+len(packet))
		binary.BigEndian.PutUint16(frame[:2], uint16(len(packet)))
		copy(frame[2:], packet)
		if _, err := c.Write(frame); err != nil {
			return nil, err
		}
		var lb [2]byte
		if _, err := io.ReadFull(c, lb[:]); err != nil {
			return nil, err
		}
		body := make([]byte, int(binary.BigEndian.Uint16(lb[:])))
		if _, err := io.ReadFull(c, body); err != nil {
			return nil, err
		}
		return body, nil
	}
	if _, err := c.Write(packet); err != nil {
		return nil, err
	}
	buf := make([]byte, 1500)
	n, err := c.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// parseOpenVPNReset verifies b is a P_CONTROL_HARD_RESET_SERVER_V2 whose ACK
// section echoes the session id we sent — the server's proof it is alive and
// answering our probe. Layout: opcode(1) own_session_id(8) ack_len(1)
// ack_ids(4*ack_len) remote_session_id(8, only when ack_len>0) packet_id(4).
func parseOpenVPNReset(b, sid []byte) error {
	if len(b) < 18 {
		return errors.New("openvpn: short reply")
	}
	if op := b[0] >> 3; op != openvpnHardResetServerV2 {
		return fmt.Errorf("openvpn: reply opcode %d, not hard_reset_server_v2", op)
	}
	ackLen := int(b[9])
	if ackLen == 0 {
		return errors.New("openvpn: server reply did not acknowledge the reset")
	}
	off := 10 + 4*ackLen // start of the echoed remote session id
	if len(b) < off+8 {
		return errors.New("openvpn: truncated server ACK")
	}
	if !bytes.Equal(b[off:off+8], sid) {
		return errors.New("openvpn: server ACK does not echo our session id")
	}
	return nil
}
