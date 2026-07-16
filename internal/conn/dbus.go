package conn

import (
	"context"

	"github.com/godbus/dbus/v5"
)

func init() { Register(dbusProtocol{}) }

const (
	// dbusDefaultAddress is the well-known system bus address.
	dbusDefaultAddress = "unix:path=/run/dbus/system_bus_socket"

	dbusCallFlags      = 0
	dbusFirstNameIndex = 0
)

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

func (dbusProtocol) Name() string       { return ProtocolNameDBus }
func (dbusProtocol) DefaultPort() int   { return defaultPortNone }
func (dbusProtocol) RequiresUser() bool { return false }

func (dbusProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	return probeBusWithDeadline(ctx, cfg, dbusProbe)
}

// probeBusWithDeadline resolves the bus address from cfg and runs probe under
// the shared deadline backstop; the prologue every D-Bus-based probe repeats.
// buildConnCheck pre-resolves the address into Socket; the fallback keeps a
// direct Probe call (e.g. from a test) resolving query/default. godbus'
// connect/call are context-aware (WithContext / CallWithContext); the outer
// backstop covers a stuck handshake.
func probeBusWithDeadline(ctx context.Context, cfg Config, probe func(ctx context.Context, addr string) (Result, error)) (Result, error) {
	addr := cfg.Socket
	if addr == "" {
		addr = DBusAddress("", cfg.Query)
	}
	return probeWithDeadline(ctx, func(ctx context.Context) (Result, error) {
		return probe(ctx, addr)
	})
}

// dbusProbe connects to the bus (auth + Hello), reads the bus id and closes.
func dbusProbe(ctx context.Context, addr string) (Result, error) {
	conn, err := dbus.Connect(addr, dbus.WithContext(ctx))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = conn.Close() }()

	var busID string
	if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetId", dbusCallFlags).Store(&busID); err != nil {
		return Result{}, err
	}
	extra := map[string]string{extraAddress: addr}
	if busID != "" {
		extra[extraBusID] = busID
	}
	if names := conn.Names(); len(names) > 0 {
		extra[extraUniqueName] = names[dbusFirstNameIndex]
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
