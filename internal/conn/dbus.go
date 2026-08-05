package conn

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
)

func init() { Register(dbusProtocol{}) }

const (
	// dbusDefaultAddress is the well-known system bus address.
	dbusDefaultAddress = "unix:path=/run/dbus/system_bus_socket"

	dbusCallFlags      = 0
	dbusFirstNameIndex = 0
	dbusTCPPrefix      = "tcp:"
	dbusTCPHostKey     = "host"
	dbusTCPPortKey     = "port"
	dbusMaxTCPPort     = 65535
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
func probeBusWithDeadline(ctx context.Context, cfg Config, probe func(ctx context.Context, cfg Config, addr string) (Result, error)) (Result, error) {
	addr := cfg.Socket
	if addr == "" {
		addr = DBusAddress("", cfg.Query)
	}
	return probeWithDeadline(ctx, func(ctx context.Context) (Result, error) {
		return probe(ctx, cfg, addr)
	})
}

// dbusProbe connects to the bus (auth + Hello), reads the bus id and closes.
func dbusProbe(ctx context.Context, cfg Config, addr string) (Result, error) {
	conn, err := connectDBus(ctx, cfg, addr)
	if err != nil {
		return Result{}, probeErr(ProtocolNameDBus, stepConnect, err)
	}
	defer func() { _ = conn.Close() }()

	var busID string
	if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetId", dbusCallFlags).Store(&busID); err != nil {
		return Result{}, probeErr(ProtocolNameDBus, stepDBusGetID, err)
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

// connectDBus establishes and completes the D-Bus auth/Hello handshake. The
// ordinary D-Bus client owns Unix-address dialing; TCP addresses with an egress
// interface are opened through probeTarget so SO_BINDTODEVICE is not bypassed.
func connectDBus(ctx context.Context, cfg Config, addr string) (*dbus.Conn, error) {
	conn, err := dialDBus(ctx, cfg, addr)
	if err != nil {
		return nil, err
	}
	if err := conn.Auth(nil); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("authenticate D-Bus connection: %w", err)
	}
	if err := conn.Hello(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("complete D-Bus Hello handshake: %w", err)
	}
	return conn, nil
}

// dialDBus creates a private D-Bus connection without the auth/Hello handshake.
// A local Unix address has no egress interface. For a TCP address, an interface
// is safety-significant: godbus dials internally, so a bound target is opened
// first and handed to dbus.NewConn instead.
func dialDBus(ctx context.Context, cfg Config, addr string) (*dbus.Conn, error) {
	if cfg.Interface == "" || !strings.HasPrefix(addr, dbusTCPPrefix) {
		conn, err := dbus.Dial(addr, dbus.WithContext(ctx))
		if err != nil {
			return nil, fmt.Errorf("dial D-Bus address %q: %w", addr, err)
		}
		return conn, nil
	}
	tcpCfg, err := dbusTCPConfig(cfg, addr)
	if err != nil {
		return nil, err
	}
	c, err := newProbeTarget(tcpCfg, defaultPortNone).openTCP(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := dbus.NewConn(c, dbus.WithContext(ctx))
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("create D-Bus connection: %w", err)
	}
	return conn, nil
}

// dbusTCPConfig parses the supported D-Bus TCP address form into a regular
// connection target. D-Bus options that alter transport semantics are rejected
// when interface binding is requested instead of silently dropping them.
func dbusTCPConfig(cfg Config, addr string) (Config, error) {
	if !strings.HasPrefix(addr, dbusTCPPrefix) {
		return Config{}, fmt.Errorf("D-Bus address %q is not TCP", addr)
	}
	var host string
	port := defaultPortNone
	for field := range strings.SplitSeq(strings.TrimPrefix(addr, dbusTCPPrefix), ",") {
		key, value, ok := strings.Cut(field, "=")
		if !ok || value == "" {
			return Config{}, fmt.Errorf("invalid D-Bus TCP address %q", addr)
		}
		switch key {
		case dbusTCPHostKey:
			if host != "" {
				return Config{}, fmt.Errorf("D-Bus TCP address %q has multiple hosts", addr)
			}
			host = value
		case dbusTCPPortKey:
			if port != defaultPortNone {
				return Config{}, fmt.Errorf("D-Bus TCP address %q has multiple ports", addr)
			}
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed <= defaultPortNone || parsed > dbusMaxTCPPort {
				return Config{}, fmt.Errorf("invalid D-Bus TCP port %q", value)
			}
			port = parsed
		default:
			return Config{}, fmt.Errorf("D-Bus TCP address option %q cannot be used with interface", key)
		}
	}
	if host == "" || port == defaultPortNone {
		return Config{}, fmt.Errorf("D-Bus TCP address %q requires host and port", addr)
	}
	return Config{Host: host, Port: port, Interface: cfg.Interface}, nil
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
