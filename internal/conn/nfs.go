package conn

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"

	"sermo/internal/units"
)

func init() { Register(nfsProtocol{}, protocolAliasNFSServer, protocolAliasNFSD) }

// NFS program number (RFC 1813). NFSv4 is TCP-only on 2049; v3 also serves UDP.
const (
	nfsProg = 100003
	nfsVers = 3
)

const (
	rpcFragmentLastMask    = 0x80000000
	rpcTCPMaxFragmentBytes = units.BytesPerMiB
)

// nfsProtocol probes an NFS server natively over ONC RPC: it sends an RPC NULL
// call to the NFS program (100003) over TCP/2049 and verifies a well-formed RPC
// reply — proof the server is up and speaking RPC. A version-mismatch reply
// (e.g. an NFSv4-only server answering a v3 NULL) still passes. No auth. Reuses
// the RPC helpers of the rpcbind probe.
type nfsProtocol struct{}

func (nfsProtocol) Name() string       { return ProtocolNameNFS }
func (nfsProtocol) DefaultPort() int   { return defaultPortNFS }
func (nfsProtocol) RequiresUser() bool { return false }

func (nfsProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	return probeRPCNull(ctx, cfg, defaultPortNFS, nfsProg, nfsVers, strconv.Itoa(nfsProg))
}

// probeRPCNull dials an ONC RPC service over TCP, sends a NULL procedure call,
// and verifies its reply. It binds egress through cfg.Interface and applies the
// context deadline, matching the behavior required by NFS-family RPC probes.
func probeRPCNull(ctx context.Context, cfg Config, defaultPort int, program, version uint32, programName string) (Result, error) {
	xid := randXID32()
	c, err := dialTCPDeadline(ctx, cfg, defaultPort)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()

	reply, err := rpcCallTCP(c, buildRPCNull(xid, program, version))
	if err != nil {
		return Result{}, err
	}
	status, err := parseRPCReply(reply, xid)
	if err != nil {
		return Result{}, err
	}
	return Result{Extra: map[string]string{extraProgram: programName, extraRPCStatus: status}}, nil
}

// rpcCallTCP sends an RPC message over a TCP connection using record marking
// (RFC 5531 §11) and reads the (possibly fragmented) reply.
func rpcCallTCP(c net.Conn, payload []byte) ([]byte, error) {
	frame := make([]byte, rpcWordBytes+len(payload))
	binary.BigEndian.PutUint32(frame, uint32(len(payload))|rpcFragmentLastMask)
	copy(frame[rpcWordBytes:], payload)
	if _, err := c.Write(frame); err != nil {
		return nil, err
	}
	var reply []byte
	for {
		var m [rpcWordBytes]byte
		if _, err := io.ReadFull(c, m[:]); err != nil {
			return nil, err
		}
		marker := binary.BigEndian.Uint32(m[:])
		n := int(marker &^ rpcFragmentLastMask)
		if n < 0 || n > rpcTCPMaxFragmentBytes {
			return nil, errors.New("nfs: RPC fragment too large")
		}
		frag := make([]byte, n)
		if _, err := io.ReadFull(c, frag); err != nil {
			return nil, err
		}
		reply = append(reply, frag...)
		if marker&rpcFragmentLastMask != 0 {
			break
		}
	}
	return reply, nil
}
