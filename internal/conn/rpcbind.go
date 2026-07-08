package conn

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
)

func init() { Register(rpcbindProtocol{}, protocolAliasPortmap, protocolAliasPortmapper) }

// ONC RPC / portmapper constants (RFC 5531, RFC 1833).
const (
	rpcCall                = 0
	rpcReply               = 1
	rpcVers                = 2
	rpcMsgAccepted         = 0
	rpcMsgDenied           = "denied"
	rpcEmptyAuthBodyLength = 0
	portmapProg            = 100000 // rpcbind / portmapper program number
	portmapVers            = 2
	rpcProcNull            = 0
	rpcAuthNone            = 0
)

const (
	rpcWordBytes                   = 4
	rpcNullCallBytes               = 40
	rpcUDPReplyBufferBytes         = 1500
	rpcReplyMinBytes               = 12
	rpcAcceptedReplyMinBytes       = 20
	rpcAcceptedReplyFixedTail      = 24
	rpcXIDOffset                   = 0
	rpcMessageTypeOffset           = 4
	rpcVersionOffset               = 8
	rpcProgramOffset               = 12
	rpcProgramVersionOffset        = 16
	rpcProcedureOffset             = 20
	rpcCredentialFlavorOffset      = 24
	rpcCredentialLengthOffset      = 28
	rpcVerifierFlavorOffset        = 32
	rpcVerifierLengthOffset        = 36
	rpcVerifierLengthOffsetInReply = 16
	rpcAcceptStatOffsetBase        = 20
)

const (
	rpcAcceptSuccess      = 0
	rpcAcceptProgUnavail  = 1
	rpcAcceptProgMismatch = 2
	rpcAcceptProcUnavail  = 3
	rpcAcceptGarbageArgs  = 4
	rpcAcceptSystemErr    = 5
)

const (
	rpcAcceptNameSuccess      = "success"
	rpcAcceptNameProgUnavail  = "prog_unavail"
	rpcAcceptNameProgMismatch = "prog_mismatch"
	rpcAcceptNameProcUnavail  = "proc_unavail"
	rpcAcceptNameGarbageArgs  = "garbage_args"
	rpcAcceptNameSystemErr    = "system_err"
)

// rpcbindProtocol probes rpcbind (the ONC RPC portmapper) natively: it sends an
// RPC NULL call to the portmapper program (100000 v2) over UDP and verifies a
// well-formed RPC reply — proof the daemon is up and speaking RPC. No auth.
type rpcbindProtocol struct{}

func (rpcbindProtocol) Name() string       { return ProtocolNameRPCBind }
func (rpcbindProtocol) DefaultPort() int   { return defaultPortRPCBind }
func (rpcbindProtocol) RequiresUser() bool { return false }

func (rpcbindProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}
	port := cfg.Port
	if port == 0 {
		port = defaultPortRPCBind
	}

	xid := randXID32()
	c, err := BindDialer(cfg.Interface).DialContext(ctx, networkUDP, hostPort(host, port))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	applyDeadline(ctx, c)

	if _, err := c.Write(buildRPCNull(xid, portmapProg, portmapVers)); err != nil {
		return Result{}, err
	}
	buf := make([]byte, rpcUDPReplyBufferBytes)
	n, err := c.Read(buf)
	if err != nil {
		return Result{}, err
	}
	status, err := parseRPCReply(buf[:n], xid)
	if err != nil {
		return Result{}, err
	}
	return Result{Extra: map[string]string{extraProgram: strconv.Itoa(portmapProg), extraRPCStatus: status}}, nil
}

// buildRPCNull builds an ONC RPC CALL for the NULL procedure of program prog
// version vers, with AUTH_NONE credentials and verifier (no procedure
// arguments). Shared by the rpcbind and nfs probes.
func buildRPCNull(xid, prog, vers uint32) []byte {
	b := make([]byte, rpcNullCallBytes)
	binary.BigEndian.PutUint32(b[rpcXIDOffset:], xid)
	binary.BigEndian.PutUint32(b[rpcMessageTypeOffset:], rpcCall)
	binary.BigEndian.PutUint32(b[rpcVersionOffset:], rpcVers)
	binary.BigEndian.PutUint32(b[rpcProgramOffset:], prog)
	binary.BigEndian.PutUint32(b[rpcProgramVersionOffset:], vers)
	binary.BigEndian.PutUint32(b[rpcProcedureOffset:], rpcProcNull)
	binary.BigEndian.PutUint32(b[rpcCredentialFlavorOffset:], rpcAuthNone)
	binary.BigEndian.PutUint32(b[rpcCredentialLengthOffset:], rpcEmptyAuthBodyLength)
	binary.BigEndian.PutUint32(b[rpcVerifierFlavorOffset:], rpcAuthNone)
	binary.BigEndian.PutUint32(b[rpcVerifierLengthOffset:], rpcEmptyAuthBodyLength)
	return b
}

// parseRPCReply validates an ONC RPC reply for xid and returns its status. Any
// well-formed reply (accepted or denied) proves an RPC responder; only a
// malformed message or an xid mismatch is an error.
func parseRPCReply(b []byte, xid uint32) (string, error) {
	if len(b) < rpcReplyMinBytes {
		return "", errors.New("short RPC reply")
	}
	if binary.BigEndian.Uint32(b[rpcXIDOffset:rpcXIDOffset+rpcWordBytes]) != xid {
		return "", errors.New("RPC reply xid mismatch")
	}
	if binary.BigEndian.Uint32(b[rpcMessageTypeOffset:rpcMessageTypeOffset+rpcWordBytes]) != rpcReply {
		return "", errors.New("not an RPC reply")
	}
	if binary.BigEndian.Uint32(b[rpcVersionOffset:rpcVersionOffset+rpcWordBytes]) != rpcMsgAccepted {
		return rpcMsgDenied, nil // MSG_DENIED — still an RPC responder
	}
	// accepted_reply: opaque_auth verf (flavor, length, body) then accept_stat.
	if len(b) < rpcAcceptedReplyMinBytes {
		return "", errors.New("short accepted RPC reply")
	}
	verfLen := int(binary.BigEndian.Uint32(b[rpcVerifierLengthOffsetInReply : rpcVerifierLengthOffsetInReply+rpcWordBytes]))
	// verfLen comes off the wire untrusted. Bound it against the bytes left for
	// the verifier body plus the 4-byte accept_stat without forming 20+verfLen
	// first: a hostile length overflows that sum to a negative offset on 32-bit
	// platforms, slipping past a `len(b) < off+4` guard into an out-of-bounds slice.
	if verfLen < 0 || verfLen > len(b)-rpcAcceptedReplyFixedTail {
		return "", errors.New("truncated accepted RPC reply")
	}
	off := rpcAcceptStatOffsetBase + verfLen
	return rpcAcceptStatName(binary.BigEndian.Uint32(b[off : off+rpcWordBytes])), nil
}

func rpcAcceptStatName(code uint32) string {
	switch code {
	case rpcAcceptSuccess:
		return rpcAcceptNameSuccess
	case rpcAcceptProgUnavail:
		return rpcAcceptNameProgUnavail
	case rpcAcceptProgMismatch:
		return rpcAcceptNameProgMismatch
	case rpcAcceptProcUnavail:
		return rpcAcceptNameProcUnavail
	case rpcAcceptGarbageArgs:
		return rpcAcceptNameGarbageArgs
	case rpcAcceptSystemErr:
		return rpcAcceptNameSystemErr
	default:
		return fmt.Sprintf("accept_stat_%d", code)
	}
}
