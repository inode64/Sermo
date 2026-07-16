package conn

func init() { Register(fail2banProtocol) }

// DefaultFail2banSocket is fail2ban-server's well-known control socket.
const DefaultFail2banSocket = "/run/fail2ban/fail2ban.sock"

// fail2banProtocol probes fail2ban-server. fail2ban speaks a Python pickle
// command protocol over a Unix socket, which is not worth reimplementing for a
// liveness check; instead the check is the connect itself — fail2ban-server
// creates and listens on the socket, so a successful connection proves it is
// running (a stale socket left by a dead server refuses the connection). It
// exchanges no commands. Socket-only (no TCP port), no auth.
var fail2banProtocol = socketOnlyProtocol{name: ProtocolNameFail2ban, socket: DefaultFail2banSocket}
