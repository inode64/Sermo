package conn

import (
	"context"

	"github.com/godbus/dbus/v5"
)

func init() { Register(udisks2Protocol{}) }

// udisks2BusName is the well-known D-Bus name for the UDisks2 daemon.
const udisks2BusName = "org.freedesktop.UDisks2"

// udisks2ManagerPath is the Manager object path on the system bus.
const udisks2ManagerPath = "/org/freedesktop/UDisks2/Manager"

// udisks2Protocol probes UDisks2 on the system D-Bus bus. It connects to the
// bus, verifies the org.freedesktop.UDisks2 name is owned, and issues
// org.freedesktop.DBus.Peer.Ping on the Manager object — proof the disk
// management service is registered and answering, not merely that dbus-daemon is
// up. Socket-based (no TCP port); optional `socket` overrides the system bus
// path, or `query` carries a full D-Bus address.
type udisks2Protocol struct{}

func (udisks2Protocol) Name() string       { return "udisks2" }
func (udisks2Protocol) DefaultPort() int   { return 0 }
func (udisks2Protocol) RequiresUser() bool { return false }

func (udisks2Protocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	addr := DBusAddress(cfg.Socket, cfg.Query)

	type probeOut struct {
		res Result
		err error
	}
	ch := make(chan probeOut, 1)
	go func() {
		res, err := udisks2Probe(ctx, addr)
		ch <- probeOut{res, err}
	}()
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case out := <-ch:
		return out.res, out.err
	}
}

func udisks2Probe(ctx context.Context, addr string) (Result, error) {
	conn, err := dbus.Dial(addr)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = conn.Close() }()

	if err := conn.Auth(nil); err != nil {
		return Result{}, err
	}
	if err := conn.Hello(); err != nil {
		return Result{}, err
	}

	var owner string
	bus := conn.Object("org.freedesktop.DBus", "/org/freedesktop/DBus")
	if err := bus.CallWithContext(ctx, "org.freedesktop.DBus.GetNameOwner", 0, udisks2BusName).Store(&owner); err != nil {
		return Result{}, err
	}

	obj := conn.Object(udisks2BusName, udisks2ManagerPath)
	if err := obj.CallWithContext(ctx, "org.freedesktop.DBus.Peer.Ping", 0).Store(); err != nil {
		return Result{}, err
	}

	return Result{Extra: map[string]string{"owner": owner}}, nil
}