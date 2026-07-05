package conn

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
)

func init() { Register(rpcbindProtocol{}, "portmap", "portmapper") }

// ONC RPC / portmapper constants (RFC 5531, RFC 1833).
const (
	rpcCall        = 0
	rpcReply       = 1
	rpcVers        = 2
	rpcMsgAccepted = 0
	portmapProg    = 100000 // rpcbind / portmapper program number
	portmapVers    = 2
	rpcProcNull    = 0
	rpcAuthNone    = 0
)

// rpcbindProtocol probes rpcbind (the ONC RPC portmapper) natively: it sends an
// RPC NULL call to the portmapper program (100000 v2) over UDP and verifies a
// well-formed RPC reply — proof the daemon is up and speaking RPC. No auth.
type rpcbindProtocol struct{}

func (rpcbindProtocol) Name() string       { return "rpcbind" }
func (rpcbindProtocol) DefaultPort() int   { return 111 }
func (rpcbindProtocol) RequiresUser() bool { return false }

func (rpcbindProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 111
	}

	xid := randXID32()
	c, err := BindDialer(cfg.Interface).DialContext(ctx, networkUDP, net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	applyDeadline(ctx, c)

	if _, err := c.Write(buildRPCNull(xid, portmapProg, portmapVers)); err != nil {
		return Result{}, err
	}
	buf := make([]byte, 1500)
	n, err := c.Read(buf)
	if err != nil {
		return Result{}, err
	}
	status, err := parseRPCReply(buf[:n], xid)
	if err != nil {
		return Result{}, err
	}
	return Result{Extra: map[string]string{extraProgram: "100000", extraRPCStatus: status}}, nil
}

// buildRPCNull builds an ONC RPC CALL for the NULL procedure of program prog
// version vers, with AUTH_NONE credentials and verifier (no procedure
// arguments). Shared by the rpcbind and nfs probes.
func buildRPCNull(xid, prog, vers uint32) []byte {
	b := make([]byte, 40)
	binary.BigEndian.PutUint32(b[0:], xid)
	binary.BigEndian.PutUint32(b[4:], rpcCall)
	binary.BigEndian.PutUint32(b[8:], rpcVers)
	binary.BigEndian.PutUint32(b[12:], prog)
	binary.BigEndian.PutUint32(b[16:], vers)
	binary.BigEndian.PutUint32(b[20:], rpcProcNull)
	binary.BigEndian.PutUint32(b[24:], rpcAuthNone) // cred flavor
	binary.BigEndian.PutUint32(b[28:], 0)           // cred length
	binary.BigEndian.PutUint32(b[32:], rpcAuthNone) // verf flavor
	binary.BigEndian.PutUint32(b[36:], 0)           // verf length
	return b
}

// parseRPCReply validates an ONC RPC reply for xid and returns its status. Any
// well-formed reply (accepted or denied) proves an RPC responder; only a
// malformed message or an xid mismatch is an error.
func parseRPCReply(b []byte, xid uint32) (string, error) {
	if len(b) < 12 {
		return "", errors.New("short RPC reply")
	}
	if binary.BigEndian.Uint32(b[0:4]) != xid {
		return "", errors.New("RPC reply xid mismatch")
	}
	if binary.BigEndian.Uint32(b[4:8]) != rpcReply {
		return "", errors.New("not an RPC reply")
	}
	if binary.BigEndian.Uint32(b[8:12]) != rpcMsgAccepted {
		return "denied", nil // MSG_DENIED — still an RPC responder
	}
	// accepted_reply: opaque_auth verf (flavor, length, body) then accept_stat.
	if len(b) < 20 {
		return "", errors.New("short accepted RPC reply")
	}
	verfLen := int(binary.BigEndian.Uint32(b[16:20]))
	// verfLen comes off the wire untrusted. Bound it against the bytes left for
	// the verifier body plus the 4-byte accept_stat without forming 20+verfLen
	// first: a hostile length overflows that sum to a negative offset on 32-bit
	// platforms, slipping past a `len(b) < off+4` guard into an out-of-bounds slice.
	if verfLen < 0 || verfLen > len(b)-24 {
		return "", errors.New("truncated accepted RPC reply")
	}
	off := 20 + verfLen
	return rpcAcceptStatName(binary.BigEndian.Uint32(b[off : off+4])), nil
}

func rpcAcceptStatName(code uint32) string {
	switch code {
	case 0:
		return "success"
	case 1:
		return "prog_unavail"
	case 2:
		return "prog_mismatch"
	case 3:
		return "proc_unavail"
	case 4:
		return "garbage_args"
	case 5:
		return "system_err"
	default:
		return fmt.Sprintf("accept_stat_%d", code)
	}
}
