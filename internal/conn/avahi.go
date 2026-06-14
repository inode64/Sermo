package conn

import (
	"context"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
)

func init() { Register(avahiProtocol{}, "avahi-daemon") }

// avahiServerRunning is AVAHI_SERVER_RUNNING (avahi-common/defs.h, the
// AvahiServerState enum).
const avahiServerRunning = 2

// avahiProtocol probes the Avahi mDNS/DNS-SD daemon natively over its D-Bus API
// (org.freedesktop.Avahi) using the pure-Go godbus client. Connecting to the
// system bus performs the SASL auth and Hello handshake; it then calls
// org.freedesktop.Avahi.Server.GetVersionString — a reply proves avahi-daemon is
// up and registered on the bus — reporting the version and, best-effort, the
// host name and server state (RUNNING).
//
// Targets the system bus (unix:path=/run/dbus/system_bus_socket) by default;
// set `socket` for a different bus socket or `query` for a full D-Bus address.
// Socket-based, no TCP port. No user/password (bus permissions govern access).
type avahiProtocol struct{}

func (avahiProtocol) Name() string       { return "avahi" }
func (avahiProtocol) DefaultPort() int   { return 0 }
func (avahiProtocol) RequiresUser() bool { return false }

func (avahiProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	// buildConnCheck pre-resolves the address into Socket; fall back here so a
	// direct Probe call (e.g. from a test) still resolves query/default.
	addr := cfg.Socket
	if addr == "" {
		addr = DBusAddress("", cfg.Query)
	}

	// godbus' connect/call are context-aware; the goroutine+select is an outer
	// backstop so a stuck handshake cannot outlive the deadline. The buffered
	// channel keeps the goroutine from leaking if ctx fires first.
	type probeOut struct {
		res Result
		err error
	}
	ch := make(chan probeOut, 1)
	go func() {
		res, err := avahiProbe(ctx, addr)
		ch <- probeOut{res, err}
	}()
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case out := <-ch:
		return out.res, out.err
	}
}

// avahiProbe connects to the bus and queries the Avahi server object.
func avahiProbe(ctx context.Context, addr string) (Result, error) {
	conn, err := dbus.Connect(addr, dbus.WithContext(ctx))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = conn.Close() }()

	obj := conn.Object("org.freedesktop.Avahi", "/")
	var versionString string
	if err := obj.CallWithContext(ctx, "org.freedesktop.Avahi.Server.GetVersionString", 0).Store(&versionString); err != nil {
		return Result{}, err
	}

	extra := map[string]string{}
	if versionString != "" {
		extra["version_string"] = versionString
	}
	// Best-effort extras: host name and server state.
	var hostname string
	if err := obj.CallWithContext(ctx, "org.freedesktop.Avahi.Server.GetHostName", 0).Store(&hostname); err == nil && hostname != "" {
		extra["hostname"] = hostname
	}
	var state int32
	if err := obj.CallWithContext(ctx, "org.freedesktop.Avahi.Server.GetState", 0).Store(&state); err == nil {
		extra["state"] = strconv.Itoa(int(state))
		if state == avahiServerRunning {
			extra["running"] = "true"
		}
	}
	return Result{Version: avahiVersion(versionString), Extra: extra}, nil
}

// avahiVersion extracts the numeric version from Avahi's version string
// ("avahi 0.8" -> "0.8"), falling back to the last whitespace-separated field.
func avahiVersion(s string) string {
	const prefix = "avahi "
	if strings.HasPrefix(s, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(s, prefix))
	}
	if f := strings.Fields(s); len(f) > 0 {
		return f[len(f)-1]
	}
	return s
}
