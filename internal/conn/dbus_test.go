package conn

import "testing"

func TestDBusRegistered(t *testing.T) {
	p, ok := Lookup("dbus")
	if !ok {
		t.Fatal("dbus not registered")
	}
	if p.DefaultPort() != 0 {
		t.Fatalf("default port = %d, want 0 (socket-based)", p.DefaultPort())
	}
	if p.RequiresUser() {
		t.Fatal("dbus must not require a user")
	}
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
