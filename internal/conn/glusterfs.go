package conn

import (
	"context"
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
	return probeRPCNull(ctx, cfg, defaultPortGlusterFS, glusterHandshakeProg, glusterHandshakeVers, glusterHandshakeName)
}
