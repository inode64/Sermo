package virt

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/digitalocean/go-libvirt"

	"sermo/internal/servicemgr"
)

type fakeClient struct {
	state libvirt.DomainState
	dom   libvirt.Domain
	calls []string
	uri   libvirt.ConnectURI
}

func (c *fakeClient) ConnectToURI(uri libvirt.ConnectURI) error {
	c.calls = append(c.calls, "connect")
	c.uri = uri
	return nil
}

func (c *fakeClient) Disconnect() error {
	c.calls = append(c.calls, "disconnect")
	return nil
}

func (c *fakeClient) DomainLookupByName(name string) (libvirt.Domain, error) {
	c.calls = append(c.calls, "lookup-name "+name)
	if name == "" {
		return libvirt.Domain{}, errors.New("empty name")
	}
	return libvirt.Domain{Name: name}, nil
}

func (c *fakeClient) DomainLookupByUUID(uuid libvirt.UUID) (libvirt.Domain, error) {
	c.calls = append(c.calls, "lookup-uuid")
	c.dom.UUID = uuid
	return c.dom, nil
}

func (c *fakeClient) DomainGetState(libvirt.Domain, uint32) (int32, int32, error) {
	c.calls = append(c.calls, "state")
	return int32(c.state), 0, nil
}

func (c *fakeClient) DomainCreate(libvirt.Domain) error {
	c.calls = append(c.calls, "create")
	return nil
}

func (c *fakeClient) DomainShutdown(libvirt.Domain) error {
	c.calls = append(c.calls, "shutdown")
	return nil
}

func (c *fakeClient) DomainResume(libvirt.Domain) error {
	c.calls = append(c.calls, "resume")
	return nil
}

func managerFor(client *fakeClient, spec Spec) Manager {
	return Manager{
		Spec: spec,
		NewClient: func(Spec, time.Duration) (Client, error) {
			return client, nil
		},
	}
}

func TestStatusFromDomainState(t *testing.T) {
	tests := []struct {
		name  string
		state libvirt.DomainState
		want  servicemgr.Status
	}{
		{name: "running", state: libvirt.DomainRunning, want: servicemgr.StatusActive},
		{name: "blocked", state: libvirt.DomainBlocked, want: servicemgr.StatusActive},
		{name: "paused", state: libvirt.DomainPaused, want: servicemgr.StatusPaused},
		{name: "suspended", state: libvirt.DomainPmsuspended, want: servicemgr.StatusPaused},
		{name: "shutoff", state: libvirt.DomainShutoff, want: servicemgr.StatusInactive},
		{name: "crashed", state: libvirt.DomainCrashed, want: servicemgr.StatusFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakeClient{state: tt.state}
			mgr := managerFor(client, Spec{URI: "qemu:///system", Domain: "vm01"})
			got, err := mgr.Status(context.Background(), "svc")
			if err != nil {
				t.Fatalf("Status() error = %v", err)
			}
			if got.Status != tt.want {
				t.Fatalf("status = %q, want %q", got.Status, tt.want)
			}
			if got.Service != "svc" || got.Unit != "vm01" || got.Backend != servicemgr.BackendLibvirt {
				t.Fatalf("service status = %+v", got)
			}
		})
	}
}

func TestManagerActions(t *testing.T) {
	tests := []struct {
		name string
		run  func(Manager) error
		want []string
	}{
		{name: "start", run: func(m Manager) error { return m.Start(context.Background(), "svc") }, want: []string{"connect", "lookup-name vm01", "create", "disconnect"}},
		{name: "stop", run: func(m Manager) error { return m.Stop(context.Background(), "svc") }, want: []string{"connect", "lookup-name vm01", "shutdown", "disconnect"}},
		{name: "resume", run: func(m Manager) error { return m.Resume(context.Background(), "svc") }, want: []string{"connect", "lookup-name vm01", "resume", "disconnect"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakeClient{}
			mgr := managerFor(client, Spec{URI: "qemu:///system", Domain: "vm01"})
			if err := tt.run(mgr); err != nil {
				t.Fatalf("action error = %v", err)
			}
			if !reflect.DeepEqual(client.calls, tt.want) {
				t.Fatalf("calls = %v, want %v", client.calls, tt.want)
			}
		})
	}
}

func TestManagerUsesUUIDLookup(t *testing.T) {
	uuid := "2b3f3d26-bb45-4b25-b65a-1e3ef86fc1a4"
	client := &fakeClient{}
	mgr := managerFor(client, Spec{URI: "qemu:///system", Domain: "vm01", UUID: uuid})
	if _, err := mgr.Status(context.Background(), "svc"); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	want := []string{"connect", "lookup-uuid", "state", "disconnect"}
	if !reflect.DeepEqual(client.calls, want) {
		t.Fatalf("calls = %v, want %v", client.calls, want)
	}
	if parsed, err := ParseUUID(uuid); err != nil || client.dom.UUID != parsed {
		t.Fatalf("uuid lookup = %v, %v", client.dom.UUID, err)
	}
}

func TestSpecFromTree(t *testing.T) {
	spec, ok, err := SpecFromTree(map[string]any{
		"control": map[string]any{
			"type":   "libvirt",
			"domain": "vm01",
		},
	})
	if err != nil || !ok {
		t.Fatalf("SpecFromTree() ok=%v err=%v", ok, err)
	}
	if spec.URI != defaultURI || spec.Socket != defaultSocket || spec.Domain != "vm01" {
		t.Fatalf("spec = %+v", spec)
	}
}

func TestSpecFromTreeRequiresDomain(t *testing.T) {
	_, ok, err := SpecFromTree(map[string]any{"control": map[string]any{"type": "libvirt"}})
	if !ok || err == nil {
		t.Fatalf("SpecFromTree() ok=%v err=%v, want domain error", ok, err)
	}
}
