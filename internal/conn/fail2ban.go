package conn

import (
	"context"
)

func init() { Register(fail2banProtocol{}) }

// DefaultFail2banSocket is fail2ban-server's well-known control socket.
const DefaultFail2banSocket = "/run/fail2ban/fail2ban.sock"

// fail2banProtocol probes fail2ban-server. fail2ban speaks a Python pickle
// command protocol over a Unix socket, which is not worth reimplementing for a
// liveness check; instead the check is the connect itself — fail2ban-server
// creates and listens on the socket, so a successful connection proves it is
// running (a stale socket left by a dead server refuses the connection). It
// exchanges no commands. Socket-only (no TCP port), no auth.
type fail2banProtocol struct{}

func (fail2banProtocol) Name() string       { return ProtocolNameFail2ban }
func (fail2banProtocol) DefaultPort() int   { return defaultPortNone }
func (fail2banProtocol) RequiresUser() bool { return false }

func (fail2banProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	socket := cfg.Socket
	if socket == "" {
		socket = DefaultFail2banSocket
	}
	c, err := dialUnix(ctx, socket)
	if err != nil {
		return Result{}, err
	}
	_ = c.Close()
	return Result{Extra: map[string]string{extraSocket: socket}}, nil
}
