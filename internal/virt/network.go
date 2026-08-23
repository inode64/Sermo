package virt

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/digitalocean/go-libvirt"

	"sermo/internal/cfgval"
	"sermo/internal/servicemgr"
)

// NetworkControlType is the service control.type value for libvirt virtual
// networks (`virsh net-start` / `net-destroy` territory).
const NetworkControlType = "libvirt-network"

// ControlKeyNetwork names the libvirt virtual network a service controls.
// ControlKeyGuardSocket/ControlKeyGuardURI select the domain-API connection
// the guest-attachment guard uses: on modular libvirt the network daemon's
// own socket serves no domain calls, so the guard dials the qemu daemon.
const (
	ControlKeyNetwork     = "network"
	ControlKeyGuardSocket = "guard_socket"
	ControlKeyGuardURI    = "guard_uri"
)

const controlPathNetwork = sectionControl + "." + ControlKeyNetwork

// DefaultNetworkSocket is the modular libvirt network daemon's local socket.
// Monolithic libvirtd answers network RPC on DefaultSocket instead.
const DefaultNetworkSocket = "/run/libvirt/virtnetworkd-sock"

// DefaultNetworkURI is the network driver connect URI; monolithic libvirtd
// and virtproxyd accept it too, so it is correct on every socket layout.
const DefaultNetworkURI = "network:///system"

// Network action labels used in operator-facing errors.
const (
	networkActionStart = "start"
	networkActionStop  = "stop"
)

// NetworkSpec describes one libvirt-controlled virtual network target. The
// guard fields name the domain-API session used to verify guest attachment;
// with a TCP Host both sessions share the same remote endpoint.
type NetworkSpec struct {
	URI         string
	Network     string
	Socket      string
	GuardSocket string
	GuardURI    string
	Host        string
	Port        int
}

// NetworkSpecFromTree reads a service's optional
// `control: {type: libvirt-network, ...}` block.
func NetworkSpecFromTree(tree map[string]any) (NetworkSpec, bool, error) {
	raw, present := tree[sectionControl]
	if !present {
		return NetworkSpec{}, false, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return NetworkSpec{}, true, errors.New("control must be a mapping")
	}
	if typ := cfgval.String(m[ControlKeyType]); typ != NetworkControlType {
		return NetworkSpec{}, true, fmt.Errorf("%s %q is not supported", controlPathType, typ)
	}
	spec := NetworkSpec{
		URI:         cfgval.String(m[ControlKeyURI]),
		Network:     cfgval.String(m[ControlKeyNetwork]),
		Socket:      cfgval.String(m[ControlKeySocket]),
		GuardSocket: cfgval.String(m[ControlKeyGuardSocket]),
		GuardURI:    cfgval.String(m[ControlKeyGuardURI]),
		Host:        cfgval.String(m[ControlKeyHost]),
	}
	if spec.URI == "" {
		spec.URI = DefaultNetworkURI
	}
	if spec.GuardURI == "" {
		spec.GuardURI = DefaultURI
	}
	if spec.Host == "" && spec.Socket == "" {
		spec.Socket = DefaultSocket
	}
	if spec.Host == "" && spec.GuardSocket == "" {
		spec.GuardSocket = spec.Socket
	}
	if p, ok := cfgval.Int(m[ControlKeyPort]); ok {
		spec.Port = p
	}
	if spec.Port == 0 {
		spec.Port = DefaultPort
	}
	if spec.Network == "" {
		return NetworkSpec{}, true, fmt.Errorf("%s is required for %s", controlPathNetwork, NetworkControlType)
	}
	return spec, true, nil
}

// NetworkClient is the small libvirt surface NetworkManager needs. Tests
// inject a fake; *libvirt.Libvirt satisfies it.
type NetworkClient interface {
	ConnectToURI(uri libvirt.ConnectURI) error
	Disconnect() error
	NetworkLookupByName(name string) (libvirt.Network, error)
	NetworkIsActive(net libvirt.Network) (int32, error)
	NetworkCreate(net libvirt.Network) error
	NetworkDestroy(net libvirt.Network) error
	NetworkGetXMLDesc(net libvirt.Network, flags uint32) (string, error)
	Domains() ([]libvirt.Domain, error)
	DomainGetState(dom libvirt.Domain, flags uint32) (int32, int32, error)
	DomainGetXMLDesc(dom libvirt.Domain, flags libvirt.DomainXMLFlags) (string, error)
}

// NetworkManager implements service management over libvirt virtual networks.
//
// Stopping (and therefore restarting) is hard-gated on guest attachment:
// destroying a network tears its bridge down and detaches every guest tap
// without reattaching them on the next start, so a network with live guest
// interfaces is never destroyed. No configuration option relaxes this.
type NetworkManager struct {
	servicemgr.ComposedRestart

	Spec NetworkSpec
	// NewClient injects the session factory for tests; socket names the
	// endpoint being dialed (network daemon or the guard's domain daemon).
	NewClient func(spec NetworkSpec, socket string, timeout time.Duration) (NetworkClient, error)
}

var _ servicemgr.Manager = NetworkManager{}

// NewNetworkManager returns a libvirt NetworkManager for spec.
func NewNetworkManager(spec NetworkSpec) NetworkManager {
	return NetworkManager{Spec: spec}
}

// Status returns the normalized state of the managed network.
func (m NetworkManager) Status(ctx context.Context, service string) (servicemgr.ServiceStatus, error) {
	status, err := runWithSession(ctx, m.Spec.URI, m.sessionClient(m.Spec.Socket), func(c NetworkClient) (servicemgr.Status, error) {
		net, err := lookupNetwork(c, m.Spec.Network)
		if err != nil {
			return "", err
		}
		active, err := c.NetworkIsActive(net)
		if err != nil {
			return "", fmt.Errorf("network %q state: %w", m.Spec.Network, err)
		}
		if active != 0 {
			return servicemgr.StatusActive, nil
		}
		return servicemgr.StatusInactive, nil
	})
	if err != nil {
		return servicemgr.ServiceStatus{}, err
	}
	return servicemgr.ServiceStatus{
		Service: service,
		Backend: servicemgr.BackendLibvirtNetwork,
		Unit:    m.Spec.Network,
		Status:  status,
	}, nil
}

// Start starts a defined, inactive libvirt network.
func (m NetworkManager) Start(ctx context.Context, _ string) error {
	return m.networkAction(ctx, networkActionStart, func(c NetworkClient, net libvirt.Network) error {
		return c.NetworkCreate(net)
	})
}

// Stop destroys the libvirt network, refusing while live guest interfaces are
// attached: they would lose connectivity and stay detached after a new start.
// The attachment check runs over the guard session (domain APIs), which on
// modular libvirt is a different daemon than the network session.
func (m NetworkManager) Stop(ctx context.Context, _ string) error {
	bridge, err := runWithSession(ctx, m.Spec.URI, m.sessionClient(m.Spec.Socket), func(c NetworkClient) (string, error) {
		net, err := lookupNetwork(c, m.Spec.Network)
		if err != nil {
			return "", err
		}
		xmlDesc, err := c.NetworkGetXMLDesc(net, 0)
		if err != nil {
			return "", fmt.Errorf("network %q xml: %w", m.Spec.Network, err)
		}
		bridge, err := networkBridgeName(xmlDesc)
		if err != nil {
			return "", fmt.Errorf("network %q xml: %w", m.Spec.Network, err)
		}
		return bridge, nil
	})
	if err != nil {
		return err
	}
	guests, err := runWithSession(ctx, m.Spec.GuardURI, m.sessionClient(m.Spec.GuardSocket), func(c NetworkClient) ([]string, error) {
		return attachedGuests(c, m.Spec.Network, bridge)
	})
	if err != nil {
		return fmt.Errorf("verify attached guests: %w", err)
	}
	if len(guests) > 0 {
		return fmt.Errorf("network %q has live guest interface(s) attached (%s); stopping it would cut guest connectivity",
			m.Spec.Network, strings.Join(guests, ", "))
	}
	return m.networkAction(ctx, networkActionStop, func(c NetworkClient, net libvirt.Network) error {
		return c.NetworkDestroy(net)
	})
}

// Reload is not meaningful for a virtual network. Restart, SupportsReload and
// ResetState come from the embedded servicemgr.ComposedRestart.
func (NetworkManager) Reload(context.Context, string) error {
	return errors.New("reload is not supported for libvirt networks")
}

// Resume is not meaningful for a virtual network: it has no paused state.
func (NetworkManager) Resume(context.Context, string) error {
	return errors.New("resume is not supported for libvirt networks")
}

func (m NetworkManager) networkAction(ctx context.Context, action string, fn func(NetworkClient, libvirt.Network) error) error {
	_, err := runWithSession(ctx, m.Spec.URI, m.sessionClient(m.Spec.Socket), func(c NetworkClient) (struct{}, error) {
		net, err := lookupNetwork(c, m.Spec.Network)
		if err != nil {
			return struct{}{}, err
		}
		if err := fn(c, net); err != nil {
			return struct{}{}, fmt.Errorf("%s network %q: %w", action, m.Spec.Network, err)
		}
		return struct{}{}, nil
	})
	return err
}

func lookupNetwork(c NetworkClient, name string) (libvirt.Network, error) {
	net, err := c.NetworkLookupByName(name)
	if err != nil {
		return libvirt.Network{}, fmt.Errorf("network %q: %w", name, err)
	}
	return net, nil
}

func (m NetworkManager) sessionClient(socket string) func(time.Duration) (NetworkClient, error) {
	return func(timeout time.Duration) (NetworkClient, error) {
		if m.NewClient != nil {
			return m.NewClient(m.Spec, socket, timeout)
		}
		return newLibvirtRPC(socket, m.Spec.Host, m.Spec.Port, timeout), nil
	}
}

// liveDomainStates are the libvirt domain states whose guests still hold their
// network taps. Anything not cleanly gone counts: a paused or blocked guest
// keeps its interfaces attached, so pausing never makes a destroy safe.
func domainHoldsInterfaces(state libvirt.DomainState) bool {
	switch state {
	case libvirt.DomainShutoff, libvirt.DomainCrashed:
		return false
	default:
		return true
	}
}

// attachedGuests returns the names of live domains with an interface on the
// network, matched by source network name or by the network's own bridge.
// Every lookup failure is returned rather than skipped: an unverifiable guest
// must block the destroy, not vanish from the check.
func attachedGuests(c NetworkClient, network, bridge string) ([]string, error) {
	domains, err := c.Domains()
	if err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}
	var guests []string
	for _, dom := range domains {
		state, _, err := c.DomainGetState(dom, 0)
		if err != nil {
			return nil, fmt.Errorf("domain %q state: %w", dom.Name, err)
		}
		if !domainHoldsInterfaces(libvirt.DomainState(state)) {
			continue
		}
		domXML, err := c.DomainGetXMLDesc(dom, 0)
		if err != nil {
			return nil, fmt.Errorf("domain %q xml: %w", dom.Name, err)
		}
		if domainUsesNetwork(domXML, network, bridge) && !slices.Contains(guests, dom.Name) {
			guests = append(guests, dom.Name)
		}
	}
	slices.Sort(guests)
	return guests, nil
}

type networkBridgeXML struct {
	Bridge struct {
		Name string `xml:"name,attr"`
	} `xml:"bridge"`
}

// networkBridgeName extracts the network's bridge device from its XML; ""
// when the network declares none. Invalid XML is not equivalent to no bridge:
// callers use this identity to prove that no live guest is attached.
func networkBridgeName(desc string) (string, error) {
	var parsed networkBridgeXML
	if err := xml.Unmarshal([]byte(desc), &parsed); err != nil {
		return "", fmt.Errorf("parse network description: %w", err)
	}
	return parsed.Bridge.Name, nil
}

type domainInterfacesXML struct {
	Devices struct {
		Interfaces []struct {
			Source struct {
				Network string `xml:"network,attr"`
				Bridge  string `xml:"bridge,attr"`
			} `xml:"source"`
		} `xml:"interface"`
	} `xml:"devices"`
}

// domainUsesNetwork reports whether the domain XML declares an interface on
// the named network, or on its bridge when the network exposes one.
func domainUsesNetwork(desc, network, bridge string) bool {
	var parsed domainInterfacesXML
	if err := xml.Unmarshal([]byte(desc), &parsed); err != nil {
		// An unparseable guest cannot be proven detached; count it as attached.
		return true
	}
	for _, iface := range parsed.Devices.Interfaces {
		if iface.Source.Network == network {
			return true
		}
		if bridge != "" && iface.Source.Bridge == bridge {
			return true
		}
	}
	return false
}
