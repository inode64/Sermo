package conn

import (
	"context"
	"strconv"
)

func init() { Register(mountdProtocol{}, protocolAliasRPCMountd, protocolAliasNFSMountd) }

// MOUNT program number (RFC 1813 appendix I). Versions 1–3 are served; the NULL
// procedure (0) exists in every version.
const (
	mountProg = 100005
	mountVers = 3
)

// mountdProtocol probes the NFS mount daemon (rpc.mountd) natively over ONC RPC:
// it sends an RPC NULL call to the MOUNT program (100005) over TCP and verifies a
// well-formed RPC reply — proof the daemon is up and speaking RPC. A
// version-mismatch reply still passes. rpc.mountd has no fixed well-known port —
// it registers a (often random) port with rpcbind — so set `port` to the daemon's
// configured port; it defaults to 20048, the common fixed mountd port. No auth.
// Reuses the RPC helpers of the rpcbind/nfs probes.
type mountdProtocol struct{}

func (mountdProtocol) Name() string       { return ProtocolNameMountd }
func (mountdProtocol) DefaultPort() int   { return defaultPortMountd }
func (mountdProtocol) RequiresUser() bool { return false }

func (mountdProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	return probeRPCNull(ctx, cfg, defaultPortMountd, mountProg, mountVers, strconv.Itoa(mountProg))
}
