package conn

import (
	"context"

	"github.com/godbus/dbus/v5"
)

func init() { Register(dbusProtocol{}) }

// dbusDefaultAddress is the well-known system bus address.
const dbusDefaultAddress = "unix:path=/run/dbus/system_bus_socket"

// dbusProtocol probes a D-Bus daemon natively over its wire protocol using the
// pure-Go github.com/godbus/dbus/v5 client. Connecting performs the SASL auth
// and the org.freedesktop.DBus.Hello handshake, which alone proves the bus is
// up; it then calls org.freedesktop.DBus.GetId to read the bus UUID. No write
// operation is performed.
//
// The target defaults to the system bus (unix:path=/run/dbus/system_bus_socket).
// Set `socket` for a different socket path, or `query` for a full D-Bus address
// (e.g. unix:abstract=..., tcp:host=...,port=...). It is socket-based and has no
// standard TCP port (a tcp: address carries its own host/port). No user/password:
// access is governed by the socket's permissions.
type dbusProtocol struct{}

func (dbusProtocol) Name() string       { return "dbus" }
func (dbusProtocol) DefaultPort() int   { return 0 }
func (dbusProtocol) RequiresUser() bool { return false }

func (dbusProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	// buildConnCheck pre-resolves the address into Socket; fall back here so a
	// direct Probe call (e.g. from a test) still resolves query/default.
	addr := cfg.Socket
	if addr == "" {
		addr = DBusAddress("", cfg.Query)
	}

	// godbus' connect/call are context-aware (WithContext / CallWithContext);
	// the goroutine+select is an outer backstop so a stuck handshake cannot
	// outlive the deadline. The buffered channel keeps the goroutine from
	// leaking if ctx fires first.
	type probeOut struct {
		res Result
		err error
	}
	ch := make(chan probeOut, 1)
	go func() {
		res, err := dbusProbe(ctx, addr)
		ch <- probeOut{res, err}
	}()
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case out := <-ch:
		return out.res, out.err
	}
}

// dbusProbe connects to the bus (auth + Hello), reads the bus id and closes.
func dbusProbe(ctx context.Context, addr string) (Result, error) {
	conn, err := dbus.Connect(addr, dbus.WithContext(ctx))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = conn.Close() }()

	var busID string
	if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetId", 0).Store(&busID); err != nil {
		return Result{}, err
	}
	extra := map[string]string{"address": addr}
	if busID != "" {
		extra["bus_id"] = busID
	}
	if names := conn.Names(); len(names) > 0 {
		extra["unique_name"] = names[0]
	}
	return Result{Extra: extra}, nil
}

// DBusAddress resolves the D-Bus address: a full address in query wins, then a
// socket path (wrapped as unix:path=), otherwise the system bus default. It is
// exported so the checks package can pre-resolve the address at build time.
func DBusAddress(socket, query string) string {
	if query != "" {
		return query
	}
	if socket != "" {
		return "unix:path=" + socket
	}
	return dbusDefaultAddress
}
