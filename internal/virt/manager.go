// Package virt provides service-manager primitives for virtual machines.
package virt

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/digitalocean/go-libvirt"
	"github.com/digitalocean/go-libvirt/socket/dialers"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
	"sermo/internal/netutil"
	"sermo/internal/servicemgr"
)

const (
	// DefaultURI is the libvirt connect URI used for local QEMU/KVM domains.
	DefaultURI = string(libvirt.QEMUSystem)
	// DefaultSocket is libvirt's traditional local control socket.
	DefaultSocket = conn.DefaultLibvirtSocket
	// DefaultQEMUSocket is the modular libvirt QEMU daemon's local socket.
	DefaultQEMUSocket = "/run/libvirt/virtqemud-sock"
	// DefaultPort is libvirt's plaintext TCP port.
	DefaultPort = conn.DefaultPortLibvirt
)

// ControlType is the service control.type value for libvirt-backed services.
const ControlType = conn.ProtocolNameLibvirt

const sectionControl = "control"

// ControlKey constants are keys inside a libvirt service control block.
const (
	ControlKeyType   = "type"
	ControlKeyURI    = "uri"
	ControlKeyDomain = "domain"
	ControlKeyUUID   = "uuid"
	ControlKeySocket = "socket"
	ControlKeyHost   = "host"
	ControlKeyPort   = "port"
)

const (
	controlPathDomain = sectionControl + "." + ControlKeyDomain
	controlPathType   = sectionControl + "." + ControlKeyType
	controlPathUUID   = sectionControl + "." + ControlKeyUUID
)

// Domain action labels used in operator-facing errors.
const (
	domainActionStart  = "start"
	domainActionStop   = "stop"
	domainActionResume = "resume"
)

// Spec describes one libvirt-controlled VM target.
type Spec struct {
	URI    string
	Domain string
	UUID   string
	Socket string
	Host   string
	Port   int
}

// SpecFromTree reads a service's optional `control: {type: libvirt, ...}` block.
func SpecFromTree(tree map[string]any) (Spec, bool, error) {
	raw, present := tree[sectionControl]
	if !present {
		return Spec{}, false, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return Spec{}, true, errors.New("control must be a mapping")
	}
	if typ := cfgval.String(m[ControlKeyType]); typ != ControlType {
		return Spec{}, true, fmt.Errorf("%s %q is not supported", controlPathType, typ)
	}
	spec := Spec{
		URI:    cfgval.String(m[ControlKeyURI]),
		Domain: cfgval.String(m[ControlKeyDomain]),
		UUID:   cfgval.String(m[ControlKeyUUID]),
		Socket: cfgval.String(m[ControlKeySocket]),
		Host:   cfgval.String(m[ControlKeyHost]),
	}
	if spec.URI == "" {
		spec.URI = DefaultURI
	}
	if spec.Host == "" && spec.Socket == "" {
		spec.Socket = DefaultSocket
	}
	if p, ok := cfgval.Int(m[ControlKeyPort]); ok {
		spec.Port = p
	}
	if spec.Port == 0 {
		spec.Port = DefaultPort
	}
	if spec.Domain == "" {
		return Spec{}, true, fmt.Errorf("%s is required for libvirt", controlPathDomain)
	}
	if spec.UUID != "" {
		if _, err := ParseUUID(spec.UUID); err != nil {
			return Spec{}, true, fmt.Errorf("%s: %w", controlPathUUID, err)
		}
	}
	return spec, true, nil
}

// Manager implements service management over libvirt domains.
type Manager struct {
	servicemgr.ComposedRestart

	Spec      Spec
	NewClient func(Spec, time.Duration) (Client, error)
}

var _ servicemgr.Manager = Manager{}

// NewManager returns a libvirt Manager for spec.
func NewManager(spec Spec) Manager {
	return Manager{Spec: spec}
}

// Client is the small libvirt surface Manager needs. Tests inject a fake.
type Client interface {
	ConnectToURI(uri libvirt.ConnectURI) error
	Disconnect() error
	Domains() ([]libvirt.Domain, error)
	DomainLookupByName(name string) (libvirt.Domain, error)
	DomainLookupByUUID(uuid libvirt.UUID) (libvirt.Domain, error)
	DomainGetState(dom libvirt.Domain, flags uint32) (int32, int32, error)
	DomainCreate(dom libvirt.Domain) error
	DomainShutdown(dom libvirt.Domain) error
	DomainResume(dom libvirt.Domain) error
}

// Status returns the normalized state of the managed domain.
func (m Manager) Status(ctx context.Context, service string) (servicemgr.ServiceStatus, error) {
	status, err := m.withDomainStatus(ctx)
	if err != nil {
		return servicemgr.ServiceStatus{}, err
	}
	return servicemgr.ServiceStatus{
		Service: service,
		Backend: servicemgr.BackendLibvirt,
		Unit:    m.Spec.Domain,
		Status:  status,
	}, nil
}

// DomainSummary is one libvirt domain discovered for the wizard.
type DomainSummary struct {
	Name   string
	Status servicemgr.Status
}

// ListDomains lists active and inactive libvirt domains with normalized status.
func ListDomains(ctx context.Context, spec Spec) ([]DomainSummary, error) {
	return listDomains(ctx, Manager{Spec: spec})
}

func listDomains(ctx context.Context, m Manager) ([]DomainSummary, error) {
	return runWithClient(ctx, m, func(c Client) ([]DomainSummary, error) {
		domains, err := c.Domains()
		if err != nil {
			return nil, fmt.Errorf("list domains: %w", err)
		}
		out := make([]DomainSummary, 0, len(domains))
		for _, dom := range domains {
			if strings.TrimSpace(dom.Name) == "" {
				continue
			}
			state, _, err := c.DomainGetState(dom, 0)
			if err != nil {
				return nil, fmt.Errorf("domain %q state: %w", dom.Name, err)
			}
			out = append(out, DomainSummary{
				Name:   dom.Name,
				Status: statusFromDomainState(libvirt.DomainState(state)),
			})
		}
		return out, nil
	})
}

// Start boots a defined, inactive libvirt domain.
func (m Manager) Start(ctx context.Context, _ string) error {
	return m.withDomainAction(ctx, domainActionStart, func(c Client, dom libvirt.Domain) error {
		return c.DomainCreate(dom)
	})
}

// Stop requests a graceful ACPI shutdown for the libvirt domain.
func (m Manager) Stop(ctx context.Context, _ string) error {
	return m.withDomainAction(ctx, domainActionStop, func(c Client, dom libvirt.Domain) error {
		return c.DomainShutdown(dom)
	})
}

// Reload is not meaningful for a VM domain. Restart, SupportsReload and
// ResetState come from the embedded servicemgr.ComposedRestart.
func (m Manager) Reload(context.Context, string) error {
	return errors.New("reload is not supported for libvirt domains")
}

// Resume unpauses a paused libvirt domain.
func (m Manager) Resume(ctx context.Context, _ string) error {
	return m.withDomainAction(ctx, domainActionResume, func(c Client, dom libvirt.Domain) error {
		return c.DomainResume(dom)
	})
}

func (m Manager) withDomainStatus(ctx context.Context) (servicemgr.Status, error) {
	return runWithClient(ctx, m, func(c Client) (servicemgr.Status, error) {
		dom, err := lookupDomain(c, m.Spec)
		if err != nil {
			return "", err
		}
		state, _, err := c.DomainGetState(dom, 0)
		if err != nil {
			return "", fmt.Errorf("domain %q state: %w", m.Spec.Domain, err)
		}
		return statusFromDomainState(libvirt.DomainState(state)), nil
	})
}

func (m Manager) withDomainAction(ctx context.Context, action string, fn func(Client, libvirt.Domain) error) error {
	_, err := runWithClient(ctx, m, func(c Client) (struct{}, error) {
		dom, err := lookupDomain(c, m.Spec)
		if err != nil {
			return struct{}{}, err
		}
		if err := fn(c, dom); err != nil {
			return struct{}{}, fmt.Errorf("%s domain %q: %w", action, m.Spec.Domain, err)
		}
		return struct{}{}, nil
	})
	if err != nil {
		return err
	}
	return nil
}

func lookupDomain(c Client, spec Spec) (libvirt.Domain, error) {
	if spec.UUID != "" {
		u, err := ParseUUID(spec.UUID)
		if err != nil {
			return libvirt.Domain{}, err
		}
		dom, err := c.DomainLookupByUUID(u)
		if err != nil {
			return libvirt.Domain{}, fmt.Errorf("domain uuid %q: %w", spec.UUID, err)
		}
		return dom, nil
	}
	dom, err := c.DomainLookupByName(spec.Domain)
	if err != nil {
		return libvirt.Domain{}, fmt.Errorf("domain %q: %w", spec.Domain, err)
	}
	return dom, nil
}

func runWithClient[T any](ctx context.Context, m Manager, fn func(Client) (T, error)) (T, error) {
	type out struct {
		value T
		err   error
	}
	ch := make(chan out, 1)
	timeout := netutil.TimeoutFromContext(ctx, conn.DefaultLibvirtTimeout)
	go func() {
		client, err := m.client(timeout)
		if err != nil {
			ch <- out{err: err}
			return
		}
		if err := client.ConnectToURI(libvirt.ConnectURI(m.Spec.URI)); err != nil {
			ch <- out{err: err}
			return
		}
		// Disconnect exactly once, and do it promptly when the caller's context
		// ends: a cancelled or timed-out call would otherwise keep the libvirt
		// connection (and its in-flight RPC) open until fn returned on its own,
		// so repeated timeouts could pile up live connections. Closing the socket
		// also interrupts the in-flight RPC, which is the intended cancellation.
		var once sync.Once
		disconnect := func() { once.Do(func() { _ = client.Disconnect() }) }
		stop := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				disconnect()
			case <-stop:
			}
		}()
		value, err := fn(client)
		close(stop)  // release the watcher
		disconnect() // close the connection before handing back the result
		ch <- out{value: value, err: err}
	}()
	select {
	case <-ctx.Done():
		var zero T
		return zero, fmt.Errorf("wait for libvirt operation: %w", ctx.Err())
	case result := <-ch:
		return result.value, result.err
	}
}

func (m Manager) client(timeout time.Duration) (Client, error) {
	if m.NewClient != nil {
		return m.NewClient(m.Spec, timeout)
	}
	if m.Spec.Host != "" {
		return libvirt.NewWithDialer(dialers.NewRemote(m.Spec.Host,
			dialers.UsePort(strconv.Itoa(m.Spec.Port)),
			dialers.WithRemoteTimeout(timeout),
		)), nil
	}
	socket := m.Spec.Socket
	if socket == "" {
		socket = DefaultSocket
	}
	return libvirt.NewWithDialer(dialers.NewLocal(
		dialers.WithSocket(filepath.Clean(socket)),
		dialers.WithLocalTimeout(timeout),
	)), nil
}

func statusFromDomainState(state libvirt.DomainState) servicemgr.Status {
	switch state {
	case libvirt.DomainRunning, libvirt.DomainBlocked:
		return servicemgr.StatusActive
	case libvirt.DomainPaused, libvirt.DomainPmsuspended:
		return servicemgr.StatusPaused
	case libvirt.DomainShutdown, libvirt.DomainShutoff, libvirt.DomainNostate:
		return servicemgr.StatusInactive
	case libvirt.DomainCrashed:
		return servicemgr.StatusFailed
	default:
		return servicemgr.StatusUnknown
	}
}

// ParseUUID accepts canonical UUIDs with hyphens or compact 32-hex strings.
func ParseUUID(value string) (libvirt.UUID, error) {
	var out libvirt.UUID
	compact := strings.ReplaceAll(strings.TrimSpace(value), "-", "")
	if len(compact) != len(out)*2 {
		return out, errors.New("expected 32 hexadecimal digits")
	}
	if _, err := hex.Decode(out[:], []byte(compact)); err != nil {
		return out, fmt.Errorf("decode UUID: %w", err)
	}
	return out, nil
}

// ValidSocketPath reports whether path is a usable absolute local socket path.
func ValidSocketPath(path string) bool {
	return path == "" || filepath.IsAbs(path)
}

// ValidHostPort reports whether the remote host and port pair is structurally valid.
func ValidHostPort(host string, port int) bool {
	if host == "" {
		return true
	}
	_, _, err := net.SplitHostPort(netutil.JoinHostPort(host, port))
	return err == nil && cfgval.ValidTCPPort(port)
}

// LocalSocketCandidates returns local libvirt sockets in preferred order.
func LocalSocketCandidates() []string {
	return []string{DefaultSocket, DefaultQEMUSocket}
}

// FirstExistingLocalSocket returns the first known local libvirt socket present
// on the host. It lets callers support both monolithic libvirtd and modular
// virtqemud deployments without probing libvirt itself when no socket exists.
func FirstExistingLocalSocket(exists func(string) (bool, error)) (string, bool, error) {
	for _, socket := range LocalSocketCandidates() {
		ok, err := exists(socket)
		if err != nil {
			return "", false, err
		}
		if ok {
			return socket, true, nil
		}
	}
	return "", false, nil
}
