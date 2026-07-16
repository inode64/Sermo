package conn

import (
	"context"
)

func init() { Register(acpidProtocol{}) }

// DefaultACPIDSocket is acpid's well-known local event socket.
const DefaultACPIDSocket = "/run/acpid.socket"

// acpidProtocol probes the ACPI event daemon (acpid). acpid is an event
// broadcaster with no request/response protocol: clients connect to its Unix
// socket and only receive lines when ACPI events occur. So the liveness check is
// the connect itself — a successful connection proves acpid is listening (a
// stale socket with no daemon refuses the connection). It reads nothing (that
// would block until an event) and there is no version. Socket-only (no TCP port),
// no auth.
type acpidProtocol struct{}

func (acpidProtocol) Name() string       { return ProtocolNameACPID }
func (acpidProtocol) DefaultPort() int   { return defaultPortNone }
func (acpidProtocol) RequiresUser() bool { return false }

func (acpidProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	return probeUnixSocket(ctx, cfg, DefaultACPIDSocket)
}
