package conn

import (
	"context"
	"net"
	"path/filepath"
	"testing"

	"sermo/internal/dockerctl"
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

func TestProbeTargetOpenTCP(t *testing.T) {
	listener, err := net.Listen(networkTCP, "127.0.0.1:0")
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

	host, portString, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := net.LookupPort(networkTCP, portString)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := newProbeTarget(Config{Host: host, Port: port}, 1234).openTCP(context.Background())
	if err != nil {
		t.Fatalf("openTCP() error = %v", err)
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

func TestResolveProtocolTarget(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		cfg      Config
		want     Config
	}{
		{
			name:     "network defaults",
			protocol: ProtocolNameRedis,
			want:     Config{Host: DefaultHost, Port: defaultPortRedis},
		},
		{
			name:     "local socket default",
			protocol: ProtocolNameDocker,
			want:     Config{Host: DefaultHost, Port: dockerctl.DefaultPort, Socket: DefaultDockerSocket},
		},
		{
			name:     "acpid socket default",
			protocol: ProtocolNameACPID,
			want:     Config{Host: DefaultHost, Socket: DefaultACPIDSocket},
		},
		{
			name:     "fail2ban socket default",
			protocol: ProtocolNameFail2ban,
			want:     Config{Host: DefaultHost, Socket: DefaultFail2banSocket},
		},
		{
			name:     "lvmpolld socket default",
			protocol: ProtocolNameLVMPolld,
			want:     Config{Host: DefaultHost, Socket: DefaultLVMPolldSocket},
		},
		{
			name:     "libvirt socket default",
			protocol: ProtocolNameLibvirt,
			want:     Config{Host: DefaultHost, Port: defaultPortLibvirt, Socket: DefaultLibvirtSocket},
		},
		{
			name:     "explicit host selects network",
			protocol: ProtocolNameDocker,
			cfg:      Config{Host: "docker.example"},
			want:     Config{Host: "docker.example", Port: dockerctl.DefaultPort},
		},
		{
			name:     "explicit target wins",
			protocol: ProtocolNameLibvirt,
			cfg:      Config{Host: "hypervisor.example", Port: 17000, Socket: "/run/custom.sock"},
			want:     Config{Host: "hypervisor.example", Port: 17000, Socket: "/run/custom.sock"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			protocol, ok := Lookup(test.protocol)
			if !ok {
				t.Fatalf("Lookup(%q) failed", test.protocol)
			}
			if got := Resolve(protocol, test.cfg); got.Host != test.want.Host || got.Port != test.want.Port || got.Socket != test.want.Socket {
				t.Errorf("Resolve() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestPrepareProtocol(t *testing.T) {
	protocol, cfg, ok := Prepare(protocolAliasValkey, Config{})
	if !ok {
		t.Fatal("Prepare(valkey) failed")
	}
	if protocol.Name() != ProtocolNameRedis {
		t.Errorf("protocol name = %q, want %q", protocol.Name(), ProtocolNameRedis)
	}
	want := Config{Host: DefaultHost, Port: defaultPortRedis}
	if cfg.Host != want.Host || cfg.Port != want.Port || cfg.Socket != want.Socket {
		t.Errorf("prepared config = %+v, want %+v", cfg, want)
	}

	input := Config{Host: "cache.example", Port: 6380}
	_, cfg, ok = Prepare(ProtocolNameRedis, input)
	if !ok || cfg.Host != input.Host || cfg.Port != input.Port {
		t.Errorf("Prepare(redis, explicit target) = %+v/%v, want %+v/true", cfg, ok, input)
	}

	if protocol, cfg, ok = Prepare("missing", input); ok || protocol != nil || cfg.Host != input.Host || cfg.Port != input.Port {
		t.Errorf("Prepare(missing) = %v/%+v/%v, want nil/input/false", protocol, cfg, ok)
	}
}
