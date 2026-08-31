package conn

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/digitalocean/go-libvirt"
	"github.com/digitalocean/go-libvirt/socket/dialers"

	"sermo/internal/netutil"
	"sermo/internal/units"
)

// DefaultLibvirtSocket is libvirt's local daemon socket.
const DefaultLibvirtSocket = "/run/libvirt/libvirt-sock"

// DefaultLibvirtTimeout is the fallback timeout for libvirt connections.
const DefaultLibvirtTimeout = 10 * time.Second

const (
	libvirtTransportSocket        = "socket"
	libvirtVersionMajorMultiplier = 1_000_000
	libvirtVersionMinorMultiplier = 1_000
)

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

func (libvirtProtocol) Name() string       { return ProtocolNameLibvirt }
func (libvirtProtocol) DefaultPort() int   { return defaultPortLibvirt }
func (libvirtProtocol) RequiresUser() bool { return false }

func (libvirtProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	target := probeTargetFor(ctx, cfg, defaultPortLibvirt)
	mode, addr, uri := libvirtTransportWithTarget(cfg, target)
	timeout := netutil.TimeoutFromContext(ctx, DefaultLibvirtTimeout)

	var l *libvirt.Libvirt
	switch mode {
	case libvirtTransportSocket:
		l = libvirt.NewWithDialer(dialers.NewLocal(
			dialers.WithSocket(addr),
			dialers.WithLocalTimeout(timeout),
		))
	default: // tcp
		l = libvirt.NewWithDialer(libvirtRemoteDialer{addr: addr, target: target, timeout: timeout})
	}

	// go-libvirt's connect/RPC calls are not context-aware; the dialer timeout
	// complements the shared context backstop.
	return probeWithDeadline(ctx, func(context.Context) (Result, error) {
		return libvirtProbe(l, uri, mode, cfg.Params[ParamKeyDomain])
	})
}

type libvirtRemoteDialer struct {
	addr    string
	target  probeTarget
	timeout time.Duration
}

func (d libvirtRemoteDialer) Dial() (net.Conn, error) {
	dialer := libvirtRemoteNetDialer(d.target, d.timeout)
	c, err := dialer.Dial(networkTCP, d.addr)
	if err != nil {
		return nil, probeErr(ProtocolNameLibvirt, stepDial, err)
	}
	return c, nil
}

func libvirtRemoteNetDialer(target probeTarget, timeout time.Duration) *net.Dialer {
	return target.dialerWithTimeout(timeout)
}

// libvirtProbe opens the connection, reads the version (and hostname), domain
// counts, node capacity and an optional single domain's state, then closes.
func libvirtProbe(l *libvirt.Libvirt, uri, mode, domain string) (Result, error) {
	if err := l.ConnectToURI(libvirt.ConnectURI(uri)); err != nil {
		return Result{}, probeErr(ProtocolNameLibvirt, stepConnect, err)
	}
	defer func() { _ = l.Disconnect() }()

	ver, err := l.ConnectGetLibVersion()
	if err != nil {
		return Result{}, probeErr(ProtocolNameLibvirt, stepVersion, err)
	}
	version := formatLibvirtVersion(ver)
	extra := map[string]string{extraURI: uri, extraLibVersion: version, extraTransport: mode}
	if hostname, err := l.ConnectGetHostname(); err == nil && hostname != "" {
		extra[ExtraKeyHostname] = hostname
	}

	// Domain counts and node capacity are best-effort: the connect + version above
	// already proved liveness, so a driver that rejects these still reports up.
	if active, err := l.ConnectNumOfDomains(); err == nil {
		extra[ExtraKeyDomainActive] = strconv.Itoa(int(active))
		if inactive, err := l.ConnectNumOfDefinedDomains(); err == nil {
			extra[ExtraKeyDomainInactive] = strconv.Itoa(int(inactive))
			extra[ExtraKeyDomainCount] = strconv.Itoa(int(active) + int(inactive))
		}
	}
	if _, mem, cpus, _, _, _, _, _, err := l.NodeGetInfo(); err == nil {
		extra[ExtraKeyNodeCPUs] = strconv.Itoa(int(cpus))
		extra[ExtraKeyNodeMemoryMB] = strconv.FormatUint(mem/units.KiBPerMiB, numericBaseDecimal)
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
		extra[ExtraKeyDomain] = domain
		extra[ExtraKeyDomainState] = s
		extra[ExtraKeyDomainRunning] = strconv.FormatBool(libvirt.DomainState(state) == libvirt.DomainRunning)
		extra[ExtraKeyFingerprint] = s // on_change tracks the VM's state
	}

	return Result{Version: version, Extra: extra}, nil
}

// libvirtDomainState maps a libvirt DomainState code to a stable lower-case name.
func libvirtDomainState(s int32) string {
	switch libvirt.DomainState(s) {
	case libvirt.DomainRunning:
		return LibvirtDomainStateRunning
	case libvirt.DomainBlocked:
		return LibvirtDomainStateBlocked
	case libvirt.DomainPaused:
		return LibvirtDomainStatePaused
	case libvirt.DomainShutdown:
		return LibvirtDomainStateShutdown
	case libvirt.DomainShutoff:
		return LibvirtDomainStateShutoff
	case libvirt.DomainCrashed:
		return LibvirtDomainStateCrashed
	case libvirt.DomainPmsuspended:
		return LibvirtDomainStatePMSuspended
	default:
		return LibvirtDomainStateNoState
	}
}

// libvirtTransport decides the transport, dial address and connect URI from the
// config: an explicit socket path, otherwise plain TCP to host:port. The connect
// URI defaults to qemu:///system.
func libvirtTransportWithTarget(cfg Config, target probeTarget) (mode, addr, uri string) {
	uri = cfg.Query
	if uri == "" {
		uri = string(libvirt.QEMUSystem)
	}
	if cfg.Socket != "" {
		return libvirtTransportSocket, cfg.Socket, uri
	}
	return networkTCP, target.address(), uri
}

// formatLibvirtVersion renders libvirt's packed version (major*1e6 + minor*1e3 +
// micro) as "major.minor.micro".
func formatLibvirtVersion(v uint64) string {
	return fmt.Sprintf("%d.%d.%d", v/libvirtVersionMajorMultiplier, (v%libvirtVersionMajorMultiplier)/libvirtVersionMinorMultiplier, v%libvirtVersionMinorMultiplier)
}
