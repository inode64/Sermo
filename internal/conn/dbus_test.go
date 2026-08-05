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
