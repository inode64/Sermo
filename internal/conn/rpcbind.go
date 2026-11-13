package conn

import (
	"context"
	"encoding/binary"
	"errors"
)

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
// RPC NULL call to the portmapper program (100000 v2) over UDP and verifies the
// program is present. No auth.
type rpcbindProtocol struct{}

func (rpcbindProtocol) Name() string       { return ProtocolNameRPCBind }
func (rpcbindProtocol) DefaultPort() int   { return defaultPortRPCBind }
func (rpcbindProtocol) RequiresUser() bool { return false }

func (rpcbindProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	return probeRPCBind(ctx, cfg)
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

// parseRPCReply validates an ONC RPC reply for xid and returns its status.
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
	off, err := rpcReplyStatusOffset(b)
	if err != nil {
		return "", err
	}

	return rpcAcceptStatName(binary.BigEndian.Uint32(b[off : off+rpcWordBytes])), nil
}

// rpcTargetProgramStatusOK reports whether status proves the requested program
// exists. A version mismatch still proves the endpoint serves the program; a
// reply before program dispatch, or any other acceptance status, does not.
func rpcTargetProgramStatusOK(status string) bool {
	return status == rpcAcceptNameSuccess || status == rpcAcceptNameProgMismatch
}

// rpcAcceptStatNames names each RPC accept_stat code.
var rpcAcceptStatNames = map[uint32]string{
	rpcAcceptSuccess:      rpcAcceptNameSuccess,
	rpcAcceptProgUnavail:  rpcAcceptNameProgUnavail,
	rpcAcceptProgMismatch: rpcAcceptNameProgMismatch,
	rpcAcceptProcUnavail:  rpcAcceptNameProcUnavail,
	rpcAcceptGarbageArgs:  rpcAcceptNameGarbageArgs,
	rpcAcceptSystemErr:    rpcAcceptNameSystemErr,
}

func rpcAcceptStatName(code uint32) string {
	return codeName(code, rpcAcceptStatNames, "accept_stat_%d")
}
