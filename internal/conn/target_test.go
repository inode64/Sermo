package conn

import (
	"context"
	"net"
	"path/filepath"
	"testing"
)

func TestProbeTargetAddress(t *testing.T) {
	tests := []struct {
		name        string
		cfg         Config
		defaultPort int
		want        string
	}{
		{
			name:        "defaults host and port",
			defaultPort: 1234,
			want:        "127.0.0.1:1234",
		},
		{
			name:        "configured IPv6 host and port",
			cfg:         Config{Host: "2001:db8::1", Port: 4321},
			defaultPort: 1234,
			want:        "[2001:db8::1]:4321",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := newProbeTarget(test.cfg, test.defaultPort).address(); got != test.want {
				t.Errorf("address() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestProbeTargetOpenStreamUsesSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "probe.sock")
	listener, err := net.Listen(networkUnix, socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()

	conn, err := newProbeTarget(Config{Socket: socket}, 1234).openStream(context.Background())
	if err != nil {
		t.Fatalf("openStream() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	serverConn := <-accepted
	_ = serverConn.Close()
}

func TestProbeTargetDialerUsesConfiguredInterface(t *testing.T) {
	if d := newProbeTarget(Config{Interface: "eth0"}, 1234).dialer(); d.Control == nil {
		t.Fatal("dialer Control is nil with an interface configured")
	}
}
