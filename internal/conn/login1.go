package conn

import "context"

func init() { Register(login1Protocol{}) }

const (
	login1BusName     = "org.freedesktop.login1"
	login1ManagerPath = "/org/freedesktop/login1"
)

// login1Protocol proves systemd-logind owns and answers its system D-Bus name.
// GetNameOwner never activates a missing service, so the probe detects failed
// activation without adding work to a congested system bus.
type login1Protocol struct{}

func (login1Protocol) Name() string       { return ProtocolNameLogin1 }
func (login1Protocol) DefaultPort() int   { return defaultPortNone }
func (login1Protocol) RequiresUser() bool { return false }

func (login1Protocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	addr := DBusAddress(cfg.Socket, cfg.Query)
	return probeWithDeadline(ctx, func(ctx context.Context) (Result, error) {
		return login1Probe(ctx, cfg, addr)
	})
}

func login1Probe(ctx context.Context, cfg Config, addr string) (Result, error) {
	conn, err := connectDBus(ctx, cfg, addr)
	if err != nil {
		return Result{}, probeErr(ProtocolNameLogin1, stepConnect, err)
	}
	defer func() { _ = conn.Close() }()

	var owner string
	bus := conn.Object(dbusBusName, dbusObjectPath)
	if err := bus.CallWithContext(ctx, dbusGetNameOwner, dbusCallFlags, login1BusName).Store(&owner); err != nil {
		return Result{}, probeErr(ProtocolNameLogin1, stepUDisks2GetNameOwner, err)
	}
	if err := conn.Object(login1BusName, login1ManagerPath).CallWithContext(ctx, dbusPeerPing, dbusCallFlags).Store(); err != nil {
		return Result{}, probeErr(ProtocolNameLogin1, stepUDisks2PeerPing, err)
	}
	return Result{Extra: map[string]string{extraOwner: owner}}, nil
}
