package conn

import (
	"context"
	"strconv"
)

func init() {
	Register(statdProtocol{}, protocolAliasRPCStatd, protocolAliasNSM, protocolAliasNFSStatd)
}

// NSM (Network Status Monitor) program number (RFC 1813 appendix; statd). Only
// version 1 exists; the NULL procedure (0) is always present.
const (
	statdProg = 100024
	statdVers = 1
)

// statdProtocol probes the NFS status-monitor daemon (rpc.statd, the NSM service
// used for NFS lock recovery) natively over ONC RPC: it sends an RPC NULL call to
// the NSM program (100024) over TCP and verifies a well-formed RPC reply — proof
// the daemon is up and speaking RPC. A version-mismatch reply still passes.
// rpc.statd has no fixed well-known port — it registers a (often random) port
// with rpcbind — so set `port` to the daemon's configured port; it defaults to
// 662, the conventional fixed statd port. No auth. Reuses the RPC helpers of the
// rpcbind/nfs probes.
type statdProtocol struct{}

func (statdProtocol) Name() string       { return ProtocolNameStatd }
func (statdProtocol) DefaultPort() int   { return defaultPortStatd }
func (statdProtocol) RequiresUser() bool { return false }

func (statdProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}
	port := cfg.Port
	if port == 0 {
		port = defaultPortStatd
	}

	xid := randXID32()
	c, err := BindDialer(cfg.Interface).DialContext(ctx, networkTCP, hostPort(host, port))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	applyDeadline(ctx, c)

	reply, err := rpcCallTCP(c, buildRPCNull(xid, statdProg, statdVers))
	if err != nil {
		return Result{}, err
	}
	status, err := parseRPCReply(reply, xid)
	if err != nil {
		return Result{}, err
	}
	return Result{Extra: map[string]string{extraProgram: strconv.Itoa(statdProg), extraRPCStatus: status}}, nil
}
