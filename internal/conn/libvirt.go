package conn

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/digitalocean/go-libvirt"
	"github.com/digitalocean/go-libvirt/socket/dialers"
)

func init() { Register(libvirtProtocol{}, "libvirtd") }

// libvirtProtocol probes a libvirt daemon (libvirtd) natively over its RPC
// protocol using the pure-Go github.com/digitalocean/go-libvirt client. It opens
// a connection (CONNECT_OPEN) to a driver URI and reads the daemon's libvirt
// version; both succeeding proves libvirtd is up and answering RPC. It then reads
// (best-effort, since the connection already proved liveness) the domain counts —
// `domains.active` (running VMs), `domains.inactive`, `domains` (total) — and node
// capacity (`node.cpus`, `node.memory_mb`), so an operator can alert on them with
// `expect`. With a `domain` selected it also reports that VM's `domain.state`
// (running/paused/shutoff/…) and `domain.running`, and tracks its state with
// `on_change`. No write operation is performed.
//
// Transport is selected by the config: a `socket` path (or the default when
// neither socket nor host is set) uses the local Unix socket; a `host` selects
// plain TCP (port 16509). TLS/SASL is out of scope. The connect URI defaults to
// qemu:///system and is overridable via `query` (e.g. lxc:/// or xen://). Local
// socket access is governed by the socket's permissions/polkit, so no
// user/password is required here.
type libvirtProtocol struct{}

func (libvirtProtocol) Name() string       { return "libvirt" }
func (libvirtProtocol) DefaultPort() int   { return 16509 }
func (libvirtProtocol) RequiresUser() bool { return false }

func (libvirtProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	mode, addr, uri := libvirtTransport(cfg)
	timeout := libvirtTimeout(ctx)

	var l *libvirt.Libvirt
	switch mode {
	case "socket":
		l = libvirt.NewWithDialer(dialers.NewLocal(
			dialers.WithSocket(addr),
			dialers.WithLocalTimeout(timeout),
		))
	default: // tcp
		l = libvirt.NewWithDialer(libvirtRemoteDialer{addr: addr, iface: cfg.Interface, timeout: timeout})
	}

	// go-libvirt's connect/RPC calls are not context-aware, so run them on a
	// goroutine and honor ctx. The dialer timeout is a backstop so the goroutine
	// cannot hang past the deadline; the buffered channel keeps it from leaking
	// if ctx fires first.
	type probeOut struct {
		res Result
		err error
	}
	ch := make(chan probeOut, 1)
	go func() {
		res, err := libvirtProbe(l, uri, mode, cfg.Params["domain"])
		ch <- probeOut{res, err}
	}()
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case out := <-ch:
		return out.res, out.err
	}
}

type libvirtRemoteDialer struct {
	addr    string
	iface   string
	timeout time.Duration
}

func (d libvirtRemoteDialer) Dial() (net.Conn, error) {
	dialer := libvirtRemoteNetDialer(d.iface, d.timeout)
	return dialer.Dial("tcp", d.addr)
}

func libvirtRemoteNetDialer(iface string, timeout time.Duration) *net.Dialer {
	d := BindDialer(iface)
	d.Timeout = timeout
	return d
}

// libvirtProbe opens the connection, reads the version (and hostname), domain
// counts, node capacity and an optional single domain's state, then closes.
func libvirtProbe(l *libvirt.Libvirt, uri, mode, domain string) (Result, error) {
	if err := l.ConnectToURI(libvirt.ConnectURI(uri)); err != nil {
		return Result{}, err
	}
	defer func() { _ = l.Disconnect() }()

	ver, err := l.ConnectGetLibVersion()
	if err != nil {
		return Result{}, err
	}
	version := formatLibvirtVersion(ver)
	extra := map[string]string{"uri": uri, "lib_version": version, "transport": mode}
	if hostname, err := l.ConnectGetHostname(); err == nil && hostname != "" {
		extra["hostname"] = hostname
	}

	// Domain counts and node capacity are best-effort: the connect + version above
	// already proved liveness, so a driver that rejects these still reports up.
	if active, err := l.ConnectNumOfDomains(); err == nil {
		extra["domains.active"] = strconv.Itoa(int(active))
		if inactive, err := l.ConnectNumOfDefinedDomains(); err == nil {
			extra["domains.inactive"] = strconv.Itoa(int(inactive))
			extra["domains"] = strconv.Itoa(int(active) + int(inactive))
		}
	}
	if _, mem, cpus, _, _, _, _, _, err := l.NodeGetInfo(); err == nil {
		extra["node.cpus"] = strconv.Itoa(int(cpus))
		extra["node.memory_mb"] = strconv.FormatUint(mem/1024, 10) // NodeGetInfo memory is KiB
	}

	// Optional single-domain state — fails the check when the VM is unknown.
	if domain != "" {
		dom, err := l.DomainLookupByName(domain)
		if err != nil {
			return Result{}, fmt.Errorf("domain %q: %w", domain, err)
		}
		state, _, err := l.DomainGetState(dom, 0)
		if err != nil {
			return Result{}, fmt.Errorf("domain %q state: %w", domain, err)
		}
		s := libvirtDomainState(state)
		extra["domain"] = domain
		extra["domain.state"] = s
		extra["domain.running"] = strconv.FormatBool(libvirt.DomainState(state) == libvirt.DomainRunning)
		extra["fingerprint"] = s // on_change tracks the VM's state
	}

	return Result{Version: version, Extra: extra}, nil
}

// libvirtDomainState maps a libvirt DomainState code to a stable lower-case name.
func libvirtDomainState(s int32) string {
	switch libvirt.DomainState(s) {
	case libvirt.DomainRunning:
		return "running"
	case libvirt.DomainBlocked:
		return "blocked"
	case libvirt.DomainPaused:
		return "paused"
	case libvirt.DomainShutdown:
		return "shutdown"
	case libvirt.DomainShutoff:
		return "shutoff"
	case libvirt.DomainCrashed:
		return "crashed"
	case libvirt.DomainPmsuspended:
		return "pmsuspended"
	default:
		return "nostate"
	}
}

// libvirtTransport decides the transport, dial address and connect URI from the
// config: an explicit socket path, otherwise plain TCP to host:port. The connect
// URI defaults to qemu:///system.
func libvirtTransport(cfg Config) (mode, addr, uri string) {
	uri = cfg.Query
	if uri == "" {
		uri = string(libvirt.QEMUSystem)
	}
	if cfg.Socket != "" {
		return "socket", cfg.Socket, uri
	}
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 16509
	}
	return "tcp", net.JoinHostPort(host, strconv.Itoa(port)), uri
}

// libvirtTimeout derives a dialer timeout from the context deadline, falling
// back to 10s when the context has none.
func libvirtTimeout(ctx context.Context) time.Duration {
	dl, ok := ctx.Deadline()
	if !ok {
		return 10 * time.Second
	}
	if d := time.Until(dl); d > 0 {
		return d
	}
	return time.Nanosecond // already past the deadline: fail fast
}

// formatLibvirtVersion renders libvirt's packed version (major*1e6 + minor*1e3 +
// micro) as "major.minor.micro".
func formatLibvirtVersion(v uint64) string {
	return fmt.Sprintf("%d.%d.%d", v/1_000_000, (v%1_000_000)/1_000, v%1_000)
}
