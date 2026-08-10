package conn

import (
	"context"
)

// glusterfsProtocol probes a GlusterFS management daemon (glusterd) over the
// TCP management endpoint on port 24007. GlusterFS deliberately leaves the
// RPC NULL actor in its handshake program unimplemented, so sending that
// generic ONC RPC probe produces a false error in glusterd's log. A connection
// is the only side-effect-free unauthenticated probe of that endpoint.
//
// This checks only a single node. gluster_cluster is the separate local check
// for authenticated, read-only cluster topology and healing status.
type glusterfsProtocol struct{}

func (glusterfsProtocol) Name() string       { return ProtocolNameGlusterFS }
func (glusterfsProtocol) DefaultPort() int   { return defaultPortGlusterFS }
func (glusterfsProtocol) RequiresUser() bool { return false }

func (glusterfsProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	c, err := probeTargetFor(ctx, cfg, defaultPortGlusterFS).openTCP(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	return Result{}, nil
}
