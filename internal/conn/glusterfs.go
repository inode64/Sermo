package conn

import (
	"context"
	"net"
	"strconv"
)

func init() { Register(glusterfsProtocol{}, protocolAliasGlusterd, protocolAliasGluster) }

// GlusterFS handshake RPC program (rpc/rpc-lib protocol-common.h).
const (
	glusterHandshakeProg = 14398633
	glusterHandshakeVers = 2
	glusterHandshakeName = "glusterfs-handshake"
)

// glusterfsProtocol probes a GlusterFS management daemon (glusterd) over the
// GlusterFS RPC protocol: it sends an RPC NULL call to the handshake program on
// TCP/24007 and verifies a well-formed RPC reply — proof that node's glusterd is
// up and speaking RPC. No auth. Reuses the ONC RPC helpers (record marking over
// TCP).
//
// This checks a single node. To alert when any node in a cluster is down,
// configure one check per node (one `host` each); the failing node's check
// fires. Cluster-wide peer status is not gathered (it needs authenticated
// management RPC).
type glusterfsProtocol struct{}

func (glusterfsProtocol) Name() string       { return ProtocolNameGlusterFS }
func (glusterfsProtocol) DefaultPort() int   { return defaultPortGlusterFS }
func (glusterfsProtocol) RequiresUser() bool { return false }

func (glusterfsProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}
	port := cfg.Port
	if port == 0 {
		port = defaultPortGlusterFS
	}

	xid := randXID32()
	c, err := BindDialer(cfg.Interface).DialContext(ctx, networkTCP, net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	applyDeadline(ctx, c)

	reply, err := rpcCallTCP(c, buildRPCNull(xid, glusterHandshakeProg, glusterHandshakeVers))
	if err != nil {
		return Result{}, err
	}
	status, err := parseRPCReply(reply, xid)
	if err != nil {
		return Result{}, err
	}
	return Result{Extra: map[string]string{extraProgram: glusterHandshakeName, extraRPCStatus: status}}, nil
}
