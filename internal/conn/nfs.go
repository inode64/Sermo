package conn

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
)

func init() { Register(nfsProtocol{}, "nfs-server", "nfsd") }

// NFS program number (RFC 1813). NFSv4 is TCP-only on 2049; v3 also serves UDP.
const (
	nfsProg = 100003
	nfsVers = 3
)

// nfsProtocol probes an NFS server natively over ONC RPC: it sends an RPC NULL
// call to the NFS program (100003) over TCP/2049 and verifies a well-formed RPC
// reply — proof the server is up and speaking RPC. A version-mismatch reply
// (e.g. an NFSv4-only server answering a v3 NULL) still passes. No auth. Reuses
// the RPC helpers of the rpcbind probe.
type nfsProtocol struct{}

func (nfsProtocol) Name() string       { return "nfs" }
func (nfsProtocol) DefaultPort() int   { return 2049 }
func (nfsProtocol) RequiresUser() bool { return false }

func (nfsProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 2049
	}

	xid := rpcXID()
	c, err := BindDialer(cfg.Interface).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}

	reply, err := rpcCallTCP(c, buildRPCNull(xid, nfsProg, nfsVers))
	if err != nil {
		return Result{}, err
	}
	status, err := parseRPCReply(reply, xid)
	if err != nil {
		return Result{}, err
	}
	return Result{Extra: map[string]string{"program": "100003", "rpc_status": status}}, nil
}

// rpcCallTCP sends an RPC message over a TCP connection using record marking
// (RFC 5531 §11) and reads the (possibly fragmented) reply.
func rpcCallTCP(c net.Conn, payload []byte) ([]byte, error) {
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, uint32(len(payload))|0x80000000) // last fragment
	if _, err := c.Write(append(hdr, payload...)); err != nil {
		return nil, err
	}
	var reply []byte
	for {
		var m [4]byte
		if _, err := io.ReadFull(c, m[:]); err != nil {
			return nil, err
		}
		marker := binary.BigEndian.Uint32(m[:])
		n := int(marker &^ 0x80000000)
		if n < 0 || n > 1<<20 {
			return nil, errors.New("nfs: RPC fragment too large")
		}
		frag := make([]byte, n)
		if _, err := io.ReadFull(c, frag); err != nil {
			return nil, err
		}
		reply = append(reply, frag...)
		if marker&0x80000000 != 0 { // last fragment
			break
		}
	}
	return reply, nil
}
