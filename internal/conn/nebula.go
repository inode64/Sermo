package conn

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strconv"
)

func init() { Register(nebulaProtocol{}, protocolAliasNebulaVPN) }

// Nebula UDP control-protocol constants (slackhq/nebula `header` package). The
// first byte packs the protocol version in the high nibble and the message type
// in the low nibble; the header is 16 bytes, big-endian.
const (
	nebulaVersion       = 1
	nebulaHeaderLen     = 16
	nebulaTypeMessage   = 1 // a tunnelled data packet
	nebulaTypeRecvError = 2 // "no tunnel for that index — re-handshake"
	nebulaReplyRecvErr  = "recv_error"
)

// nebulaProtocol probes a Nebula mesh-VPN node natively over its UDP control
// protocol. A real tunnel needs a CA-signed certificate, but a node answers a
// data packet for an unknown tunnel index with a plaintext "recv_error" telling
// the sender to re-handshake — so the probe sends a Message packet (type 1)
// carrying a random remote index and verifies the node replies with a recv_error
// (type 2) echoing that index. That proves the node is up and speaking the Nebula
// protocol without any credentials. Default UDP port 4242. No auth.
//
// The recv_error reply is governed by `listen.send_recv_error` (default
// "always"); a node set to "never" — or "private" when probed from a public
// address — stays silent and reads as down.
type nebulaProtocol struct{}

func (nebulaProtocol) Name() string       { return ProtocolNameNebula }
func (nebulaProtocol) DefaultPort() int   { return defaultPortNebula }
func (nebulaProtocol) RequiresUser() bool { return false }

func (nebulaProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}
	port := cfg.Port
	if port == 0 {
		port = defaultPortNebula
	}

	// A random 32-bit tunnel index the node won't have (reuses the shared random
	// uint32 helper); the recv_error echoes it back so we can match the reply.
	index := randXID32()

	c, err := BindDialer(cfg.Interface).DialContext(ctx, networkUDP, net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	applyDeadline(ctx, c)

	if _, err := c.Write(nebulaMessage(index)); err != nil {
		return Result{}, err
	}
	buf := make([]byte, 64)
	n, err := c.Read(buf)
	if err != nil {
		return Result{}, err
	}
	if err := parseNebulaRecvError(buf[:n], index); err != nil {
		return Result{}, err
	}
	return Result{Extra: map[string]string{extraReply: nebulaReplyRecvErr}}, nil
}

// nebulaMessage builds a 16-byte Nebula Message header (type 1) with the given
// remote index and a zero message counter — enough to make a node that has no
// tunnel for that index answer with a recv_error.
func nebulaMessage(index uint32) []byte {
	b := make([]byte, nebulaHeaderLen)
	b[0] = nebulaVersion<<4 | nebulaTypeMessage
	binary.BigEndian.PutUint32(b[4:8], index)
	return b
}

// parseNebulaRecvError verifies b is a Nebula recv_error (type 2) header echoing
// the index we sent — the node's proof it is alive and speaking Nebula.
func parseNebulaRecvError(b []byte, index uint32) error {
	if len(b) < nebulaHeaderLen {
		return errors.New("nebula: short reply")
	}
	if b[0]>>4 != nebulaVersion {
		return errors.New("nebula: unexpected protocol version")
	}
	if b[0]&0x0f != nebulaTypeRecvError {
		return errors.New("nebula: reply is not a recv_error")
	}
	if binary.BigEndian.Uint32(b[4:8]) != index {
		return errors.New("nebula: recv_error index mismatch")
	}
	return nil
}
