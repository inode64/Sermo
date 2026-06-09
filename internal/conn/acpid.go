package conn

import (
	"context"
	"net"
)

func init() { Register(acpidProtocol{}) }

// acpidDefaultSocket is acpid's well-known local event socket.
const acpidDefaultSocket = "/var/run/acpid.socket"

// acpidProtocol probes the ACPI event daemon (acpid). acpid is an event
// broadcaster with no request/response protocol: clients connect to its Unix
// socket and only receive lines when ACPI events occur. So the liveness check is
// the connect itself — a successful connection proves acpid is listening (a
// stale socket with no daemon refuses the connection). It reads nothing (that
// would block until an event) and there is no version. Socket-only (no TCP port),
// no auth.
type acpidProtocol struct{}

func (acpidProtocol) Name() string       { return "acpid" }
func (acpidProtocol) DefaultPort() int   { return 0 }
func (acpidProtocol) RequiresUser() bool { return false }

func (acpidProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	socket := cfg.Socket
	if socket == "" {
		socket = acpidDefaultSocket
	}
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return Result{}, err
	}
	_ = c.Close()
	return Result{Extra: map[string]string{"socket": socket}}, nil
}
