package virt

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/digitalocean/go-libvirt"

	"sermo/internal/servicemgr"
)

const (
	testNetXML = `<network><name>default</name><bridge name='virbr0' stp='on'/><ip address='192.168.122.1' netmask='255.255.255.0'/></network>`

	domOnNetworkXML = `<domain><devices><interface type='network'><source network='default'/></interface></devices></domain>`
	domOnBridgeXML  = `<domain><devices><interface type='bridge'><source bridge='virbr0'/></interface></devices></domain>`
	domElsewhereXML = `<domain><devices><interface type='bridge'><source bridge='vbr0'/></interface></devices></domain>`
)

type fakeNetworkClient struct {
	active   int32
	netXML   string
	domains  []libvirt.Domain
	states   map[string]libvirt.DomainState
	domXML   map[string]string
	calls    []string
	sockets  []string
	xmlErr   error
	stateErr error
}

func (c *fakeNetworkClient) ConnectToURI(libvirt.ConnectURI) error {
	c.calls = append(c.calls, "connect")
	return nil
}

func (c *fakeNetworkClient) Disconnect() error {
	c.calls = append(c.calls, "disconnect")
	return nil
}

func (c *fakeNetworkClient) NetworkLookupByName(name string) (libvirt.Network, error) {
	c.calls = append(c.calls, "lookup "+name)
	if name == "" {
		return libvirt.Network{}, errors.New("empty name")
	}
	return libvirt.Network{Name: name}, nil
}

func (c *fakeNetworkClient) NetworkIsActive(libvirt.Network) (int32, error) {
	c.calls = append(c.calls, "is-active")
	return c.active, nil
}

func (c *fakeNetworkClient) NetworkCreate(libvirt.Network) error {
	c.calls = append(c.calls, "net-create")
	return nil
}

func (c *fakeNetworkClient) NetworkDestroy(libvirt.Network) error {
	c.calls = append(c.calls, "net-destroy")
	return nil
}

func (c *fakeNetworkClient) NetworkGetXMLDesc(libvirt.Network, uint32) (string, error) {
	c.calls = append(c.calls, "net-xml")
	if c.xmlErr != nil {
		return "", c.xmlErr
	}
	return c.netXML, nil
}

func (c *fakeNetworkClient) Domains() ([]libvirt.Domain, error) {
	c.calls = append(c.calls, "domains")
	return c.domains, nil
}

func (c *fakeNetworkClient) DomainGetState(dom libvirt.Domain, _ uint32) (int32, int32, error) {
	if c.stateErr != nil {
		return 0, 0, c.stateErr
	}
	return int32(c.states[dom.Name]), 0, nil
}

func (c *fakeNetworkClient) DomainGetXMLDesc(dom libvirt.Domain, _ libvirt.DomainXMLFlags) (string, error) {
	return c.domXML[dom.Name], nil
}

func networkManagerWith(client *fakeNetworkClient) NetworkManager {
	m := NewNetworkManager(NetworkSpec{
		URI:         DefaultNetworkURI,
		Network:     "default",
		Socket:      DefaultNetworkSocket,
		GuardSocket: DefaultQEMUSocket,
		GuardURI:    DefaultURI,
	})
	m.NewClient = func(_ NetworkSpec, socket string, _ time.Duration) (NetworkClient, error) {
		client.sockets = append(client.sockets, socket)
		return client, nil
	}
	return m
}

func TestNetworkManagerStatus(t *testing.T) {
	for _, tc := range []struct {
		active int32
		want   servicemgr.Status
	}{
		{active: 1, want: servicemgr.StatusActive},
		{active: 0, want: servicemgr.StatusInactive},
	} {
		client := &fakeNetworkClient{active: tc.active, netXML: testNetXML}
		status, err := networkManagerWith(client).Status(context.Background(), "libvirt-net-default")
		if err != nil {
			t.Fatalf("Status() error = %v", err)
		}
		if status.Status != tc.want || status.Backend != servicemgr.BackendLibvirtNetwork || status.Unit != "default" {
			t.Fatalf("Status() = %+v, want status %s on backend %s", status, tc.want, servicemgr.BackendLibvirtNetwork)
		}
	}
}

func TestNetworkManagerStartCreates(t *testing.T) {
	client := &fakeNetworkClient{netXML: testNetXML}
	if err := networkManagerWith(client).Start(context.Background(), "libvirt-net-default"); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !slicesContains(client.calls, "net-create") {
		t.Fatalf("calls = %v, want net-create", client.calls)
	}
}

// TestNetworkManagerStopRefusesAttachedGuests is the hard invariant: a network
// with live guest interfaces — matched by source network or by the network's
// bridge, and including paused guests — is never destroyed.
func TestNetworkManagerStopRefusesAttachedGuests(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state libvirt.DomainState
		xml   string
	}{
		{name: "running on network", state: libvirt.DomainRunning, xml: domOnNetworkXML},
		{name: "running on bridge", state: libvirt.DomainRunning, xml: domOnBridgeXML},
		{name: "paused on network", state: libvirt.DomainPaused, xml: domOnNetworkXML},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeNetworkClient{
				netXML:  testNetXML,
				domains: []libvirt.Domain{{Name: "kvm1"}},
				states:  map[string]libvirt.DomainState{"kvm1": tc.state},
				domXML:  map[string]string{"kvm1": tc.xml},
			}
			err := networkManagerWith(client).Stop(context.Background(), "libvirt-net-default")
			if err == nil || !strings.Contains(err.Error(), "kvm1") {
				t.Fatalf("Stop() error = %v, want refusal naming kvm1", err)
			}
			if slicesContains(client.calls, "net-destroy") {
				t.Fatalf("calls = %v: destroy ran despite attached guest", client.calls)
			}
		})
	}
}

func TestNetworkManagerStopGuardsOverGuardSocket(t *testing.T) {
	client := &fakeNetworkClient{netXML: testNetXML}
	if err := networkManagerWith(client).Stop(context.Background(), "libvirt-net-default"); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	found := false
	for _, s := range client.sockets {
		if s == DefaultQEMUSocket {
			found = true
		}
	}
	if !found {
		t.Fatalf("sockets dialed = %v, want the guard session on %s", client.sockets, DefaultQEMUSocket)
	}
}

func TestNetworkManagerStopDestroysIdleNetwork(t *testing.T) {
	client := &fakeNetworkClient{
		netXML:  testNetXML,
		domains: []libvirt.Domain{{Name: "kvm1"}, {Name: "off"}},
		states: map[string]libvirt.DomainState{
			"kvm1": libvirt.DomainRunning,
			"off":  libvirt.DomainShutoff,
		},
		domXML: map[string]string{
			"kvm1": domElsewhereXML,
			"off":  domOnNetworkXML, // shut off: holds no tap
		},
	}
	if err := networkManagerWith(client).Stop(context.Background(), "libvirt-net-default"); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !slicesContains(client.calls, "net-destroy") {
		t.Fatalf("calls = %v, want net-destroy", client.calls)
	}
}

// TestNetworkManagerStopFailsClosedOnUnverifiableGuests: when the network XML
// cannot be read the bridge attachment cannot be proven absent, so the destroy
// must not run.
func TestNetworkManagerStopFailsClosedOnUnverifiableGuests(t *testing.T) {
	client := &fakeNetworkClient{
		netXML:  testNetXML,
		xmlErr:  errors.New("xml unavailable"),
		domains: []libvirt.Domain{{Name: "kvm1"}},
		states:  map[string]libvirt.DomainState{"kvm1": libvirt.DomainRunning},
		domXML:  map[string]string{"kvm1": domElsewhereXML},
	}
	err := networkManagerWith(client).Stop(context.Background(), "libvirt-net-default")
	if err == nil {
		t.Fatal("Stop() = nil, want fail-closed error")
	}
	if slicesContains(client.calls, "net-destroy") {
		t.Fatalf("calls = %v: destroy ran without verification", client.calls)
	}
}

func TestNetworkManagerUnparseableDomainCountsAsAttached(t *testing.T) {
	client := &fakeNetworkClient{
		netXML:  testNetXML,
		domains: []libvirt.Domain{{Name: "kvm1"}},
		states:  map[string]libvirt.DomainState{"kvm1": libvirt.DomainRunning},
		domXML:  map[string]string{"kvm1": "<domain><devices><interface"},
	}
	err := networkManagerWith(client).Stop(context.Background(), "libvirt-net-default")
	if err == nil || !strings.Contains(err.Error(), "kvm1") {
		t.Fatalf("Stop() error = %v, want refusal naming the unparseable guest", err)
	}
}

func TestNetworkManagerReloadAndResumeUnsupported(t *testing.T) {
	m := networkManagerWith(&fakeNetworkClient{netXML: testNetXML})
	if err := m.Reload(context.Background(), "x"); err == nil {
		t.Fatal("Reload() = nil, want unsupported error")
	}
	if err := m.Resume(context.Background(), "x"); err == nil {
		t.Fatal("Resume() = nil, want unsupported error")
	}
	if ok, err := m.SupportsReload(context.Background(), "x"); err != nil || ok {
		t.Fatalf("SupportsReload() = %v, %v; want false, nil", ok, err)
	}
}

func TestNetworkSpecFromTree(t *testing.T) {
	tree := map[string]any{"control": map[string]any{
		"type": NetworkControlType, "network": "default",
	}}
	spec, controlled, err := NetworkSpecFromTree(tree)
	if err != nil || !controlled {
		t.Fatalf("NetworkSpecFromTree() = %v, %v", controlled, err)
	}
	if spec.Network != "default" || spec.URI != DefaultNetworkURI || spec.Socket != DefaultSocket || spec.Port != DefaultPort {
		t.Fatalf("spec = %+v, want defaults applied", spec)
	}
	if spec.GuardURI != DefaultURI || spec.GuardSocket != DefaultSocket {
		t.Fatalf("spec = %+v, want guard defaults following the network socket", spec)
	}
	if _, _, err := NetworkSpecFromTree(map[string]any{"control": map[string]any{"type": NetworkControlType}}); err == nil {
		t.Fatal("NetworkSpecFromTree() without network = nil error, want required-network error")
	}
}

func slicesContains(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}
