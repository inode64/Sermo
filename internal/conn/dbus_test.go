package conn

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"

	"sermo/internal/cfgval"
)

type recordingDBusCaller struct {
	method string
	flags  dbus.Flags
	args   []any
	body   []any
	err    error
}

func (c *recordingDBusCaller) CallWithContext(_ context.Context, method string, flags dbus.Flags, args ...any) *dbus.Call {
	c.method, c.flags, c.args = method, flags, args
	return &dbus.Call{Body: c.body, Err: c.err}
}

func TestDBusAddress(t *testing.T) {
	// A full address in query wins over socket.
	if got := DBusAddress("/run/dbus/system_bus_socket", "tcp:host=10.0.0.5,port=44444"); got != "tcp:host=10.0.0.5,port=44444" {
		t.Fatalf("query should win, got %q", got)
	}
	// A socket path is wrapped as unix:path=.
	if got := DBusAddress("/run/dbus/system_bus_socket", ""); got != "unix:path=/run/dbus/system_bus_socket" {
		t.Fatalf("socket wrap = %q", got)
	}
	// Nothing set -> the system bus default.
	if got := DBusAddress("", ""); got != dbusDefaultAddress {
		t.Fatalf("default = %q, want %q", got, dbusDefaultAddress)
	}
}

func TestDBusTCPConfig(t *testing.T) {
	tests := []struct {
		name    string
		address string
		want    Config
		wantErr bool
	}{
		{
			name:    "host and port",
			address: "tcp:host=10.0.0.5,port=44444",
			want:    Config{Host: "10.0.0.5", Port: 44444, Interface: "eth0"},
		},
		{
			name:    "minimum port",
			address: fmt.Sprintf("tcp:host=10.0.0.5,port=%d", cfgval.MinTCPPort),
			want:    Config{Host: "10.0.0.5", Port: cfgval.MinTCPPort, Interface: "eth0"},
		},
		{
			name:    "maximum port",
			address: fmt.Sprintf("tcp:host=10.0.0.5,port=%d", cfgval.MaxTCPPort),
			want:    Config{Host: "10.0.0.5", Port: cfgval.MaxTCPPort, Interface: "eth0"},
		},
		{
			name:    "port below minimum",
			address: fmt.Sprintf("tcp:host=10.0.0.5,port=%d", cfgval.MinTCPPort-1),
			wantErr: true,
		},
		{
			name:    "port above maximum",
			address: fmt.Sprintf("tcp:host=10.0.0.5,port=%d", cfgval.MaxTCPPort+1),
			wantErr: true,
		},
		{
			name:    "missing port",
			address: "tcp:host=10.0.0.5",
			wantErr: true,
		},
		{
			name:    "unsupported option",
			address: "tcp:host=10.0.0.5,port=44444,family=ipv4",
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := dbusTCPConfig(Config{Interface: "eth0"}, test.address)
			if test.wantErr {
				if err == nil {
					t.Fatal("dbusTCPConfig() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("dbusTCPConfig() error = %v", err)
			}
			if got.Host != test.want.Host || got.Port != test.want.Port || got.Interface != test.want.Interface {
				t.Errorf("dbusTCPConfig() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestValidateDBusTarget(t *testing.T) {
	tests := []struct {
		name    string
		target  DBusTarget
		wantErr string
	}{
		{name: "bus only"},
		{name: "named service", target: DBusTarget{BusName: "org.libvirt", ObjectPath: "/org/libvirt"}},
		{name: "introspect", target: DBusTarget{BusName: "org.gnome.DisplayManager", ObjectPath: "/org/gnome/DisplayManager/Manager", Probe: DBusProbeIntrospect, Interface: "org.gnome.DisplayManager.Manager"}},
		{name: "property", target: DBusTarget{BusName: "net.hadess.PowerProfiles", ObjectPath: "/net/hadess/PowerProfiles", Probe: DBusProbeProperty, Interface: "net.hadess.PowerProfiles", Property: "ActiveProfile"}},
		{name: "missing bus name", target: DBusTarget{ObjectPath: "/org/libvirt"}, wantErr: "bus_name is required"},
		{name: "missing object path", target: DBusTarget{BusName: "org.libvirt"}, wantErr: "object_path is required"},
		{name: "single component", target: DBusTarget{BusName: "libvirt", ObjectPath: "/org/libvirt"}, wantErr: "not a valid well-known"},
		{name: "unique name", target: DBusTarget{BusName: ":1.42", ObjectPath: "/org/libvirt"}, wantErr: "not a valid well-known"},
		{name: "digit-leading element", target: DBusTarget{BusName: "org.1libvirt", ObjectPath: "/org/libvirt"}, wantErr: "not a valid well-known"},
		{name: "invalid character", target: DBusTarget{BusName: "org.libvirt!", ObjectPath: "/org/libvirt"}, wantErr: "not a valid well-known"},
		{name: "too long", target: DBusTarget{BusName: "org." + strings.Repeat("a", dbusMaxBusNameLen), ObjectPath: "/org/libvirt"}, wantErr: "not a valid well-known"},
		{name: "invalid object path", target: DBusTarget{BusName: "org.libvirt", ObjectPath: "org/libvirt"}, wantErr: "not a valid D-Bus object path"},
		{name: "unknown probe", target: DBusTarget{BusName: "org.libvirt", ObjectPath: "/org/libvirt", Probe: "call"}, wantErr: "probe must be"},
		{name: "peer interface", target: DBusTarget{BusName: "org.libvirt", ObjectPath: "/org/libvirt", Interface: "org.libvirt.Connect"}, wantErr: "dbus_interface is not supported"},
		{name: "introspect property", target: DBusTarget{BusName: "org.libvirt", ObjectPath: "/org/libvirt", Probe: DBusProbeIntrospect, Property: "State"}, wantErr: "property is only supported"},
		{name: "property missing interface", target: DBusTarget{BusName: "org.libvirt", ObjectPath: "/org/libvirt", Probe: DBusProbeProperty, Property: "State"}, wantErr: "dbus_interface is required"},
		{name: "property missing property", target: DBusTarget{BusName: "org.libvirt", ObjectPath: "/org/libvirt", Probe: DBusProbeProperty, Interface: "org.libvirt.Connect"}, wantErr: "property is required"},
		{name: "invalid interface", target: DBusTarget{BusName: "org.libvirt", ObjectPath: "/org/libvirt", Probe: DBusProbeIntrospect, Interface: "org.libvirt-bad.Connect"}, wantErr: "not a valid D-Bus interface"},
		{name: "invalid property", target: DBusTarget{BusName: "org.libvirt", ObjectPath: "/org/libvirt", Probe: DBusProbeProperty, Interface: "org.libvirt.Connect", Property: "1State"}, wantErr: "not a valid D-Bus property"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateDBusTarget(test.target)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateDBusTarget() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ValidateDBusTarget() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestProbeDBusServiceReadOnlyModes(t *testing.T) {
	tests := []struct {
		name       string
		target     DBusTarget
		body       []any
		wantMethod string
		wantArgs   []any
		wantExtra  map[string]string
	}{
		{
			name:       "introspect interface",
			target:     DBusTarget{BusName: "org.gnome.DisplayManager", ObjectPath: "/org/gnome/DisplayManager/Manager", Probe: DBusProbeIntrospect, Interface: "org.gnome.DisplayManager.Manager"},
			body:       []any{`<node><interface name="org.gnome.DisplayManager.Manager"/></node>`},
			wantMethod: dbusIntrospect,
			wantExtra:  map[string]string{ExtraKeyDBusProbe: DBusProbeIntrospect, ExtraKeyDBusInterface: "org.gnome.DisplayManager.Manager"},
		},
		{
			name:       "scalar property",
			target:     DBusTarget{BusName: "net.hadess.PowerProfiles", ObjectPath: "/net/hadess/PowerProfiles", Probe: DBusProbeProperty, Interface: "net.hadess.PowerProfiles", Property: "ActiveProfile"},
			body:       []any{dbus.MakeVariant("balanced")},
			wantMethod: dbusPropertiesGet,
			wantArgs:   []any{"net.hadess.PowerProfiles", "ActiveProfile"},
			wantExtra: map[string]string{
				ExtraKeyDBusProbe: DBusProbeProperty, ExtraKeyDBusInterface: "net.hadess.PowerProfiles",
				ExtraKeyDBusProperty: "ActiveProfile", ExtraKeyDBusPropertyValue: "balanced",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bus := &recordingDBusCaller{body: []any{":1.42"}}
			object := &recordingDBusCaller{body: test.body}
			owner, extra, err := probeDBusService(context.Background(), bus, func(string, dbus.ObjectPath) dbusMethodCaller {
				return object
			}, test.target)
			if err != nil {
				t.Fatalf("probeDBusService() error = %v", err)
			}
			if owner != ":1.42" || object.method != test.wantMethod || object.flags != dbus.FlagNoAutoStart {
				t.Fatalf("owner/call = %q %q flags=%v", owner, object.method, object.flags)
			}
			if fmt.Sprint(object.args) != fmt.Sprint(test.wantArgs) {
				t.Fatalf("args = %v, want %v", object.args, test.wantArgs)
			}
			for key, want := range test.wantExtra {
				if extra[key] != want {
					t.Errorf("extra[%q] = %q, want %q", key, extra[key], want)
				}
			}
		})
	}
}

func TestProbeDBusServiceReadOnlyModeFailures(t *testing.T) {
	tests := []struct {
		name    string
		target  DBusTarget
		body    []any
		wantErr string
	}{
		{
			name:    "malformed introspection XML",
			target:  DBusTarget{BusName: "org.example.Service", ObjectPath: "/org/example/Service", Probe: DBusProbeIntrospect},
			body:    []any{"<node>"},
			wantErr: "parse D-Bus introspection XML",
		},
		{
			name:    "missing interface",
			target:  DBusTarget{BusName: "org.example.Service", ObjectPath: "/org/example/Service", Probe: DBusProbeIntrospect, Interface: "org.example.Missing"},
			body:    []any{`<node><interface name="org.example.Service"/></node>`},
			wantErr: "is not exported",
		},
		{
			name:    "complex property",
			target:  DBusTarget{BusName: "org.example.Service", ObjectPath: "/org/example/Service", Probe: DBusProbeProperty, Interface: "org.example.Service", Property: "Items"},
			body:    []any{dbus.MakeVariant([]string{"one", "two"})},
			wantErr: "is not a supported scalar",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bus := &recordingDBusCaller{body: []any{":1.42"}}
			object := &recordingDBusCaller{body: test.body}
			_, _, err := probeDBusService(context.Background(), bus, func(string, dbus.ObjectPath) dbusMethodCaller {
				return object
			}, test.target)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("probeDBusService() error = %v, want %q", err, test.wantErr)
			}
			if object.flags != dbus.FlagNoAutoStart {
				t.Fatalf("flags = %v, want FlagNoAutoStart", object.flags)
			}
		})
	}
}

func TestProbeDBusServiceUsesUniqueOwnerWithoutActivation(t *testing.T) {
	bus := &recordingDBusCaller{body: []any{":1.42"}}
	peer := &recordingDBusCaller{}
	var destination string
	var path dbus.ObjectPath
	owner, _, err := probeDBusService(context.Background(), bus, func(gotDestination string, gotPath dbus.ObjectPath) dbusMethodCaller {
		destination, path = gotDestination, gotPath
		return peer
	}, DBusTarget{BusName: "org.libvirt", ObjectPath: "/org/libvirt"})
	if err != nil {
		t.Fatalf("probeDBusService() error = %v", err)
	}
	if owner != ":1.42" || destination != owner || path != "/org/libvirt" {
		t.Fatalf("owner/destination/path = %q/%q/%q", owner, destination, path)
	}
	if bus.method != dbusGetNameOwner || bus.flags != dbus.FlagNoAutoStart || len(bus.args) != 1 || bus.args[0] != "org.libvirt" {
		t.Fatalf("GetNameOwner call = method %q flags %v args %v", bus.method, bus.flags, bus.args)
	}
	if peer.method != dbusPeerPing || peer.flags != dbus.FlagNoAutoStart || len(peer.args) != 0 {
		t.Fatalf("Peer.Ping call = method %q flags %v args %v", peer.method, peer.flags, peer.args)
	}
}

func TestProbeDBusServiceFailures(t *testing.T) {
	tests := []struct {
		name    string
		bus     *recordingDBusCaller
		peer    *recordingDBusCaller
		wantErr string
	}{
		{name: "owner lookup", bus: &recordingDBusCaller{err: errors.New("no owner")}, peer: &recordingDBusCaller{}, wantErr: "GetNameOwner"},
		{name: "empty owner", bus: &recordingDBusCaller{body: []any{""}}, peer: &recordingDBusCaller{}, wantErr: "empty owner"},
		{name: "ping", bus: &recordingDBusCaller{body: []any{":1.42"}}, peer: &recordingDBusCaller{err: errors.New("peer gone")}, wantErr: "Peer.Ping"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := probeDBusService(context.Background(), test.bus, func(string, dbus.ObjectPath) dbusMethodCaller {
				return test.peer
			}, DBusTarget{BusName: "org.libvirt", ObjectPath: "/org/libvirt"})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("probeDBusService() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}
