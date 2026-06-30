package conn

import "testing"

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
