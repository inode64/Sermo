package conn

import "testing"

func TestLibvirtRegistered(t *testing.T) {
	for _, name := range []string{"libvirt", "libvirtd"} {
		p, ok := Lookup(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if p.DefaultPort() != 16509 {
			t.Fatalf("%s default port = %d, want 16509", name, p.DefaultPort())
		}
		if p.RequiresUser() {
			t.Fatalf("%s must not require a user", name)
		}
	}
}

func TestFormatLibvirtVersion(t *testing.T) {
	cases := map[uint64]string{
		9000000: "9.0.0",
		9008015: "9.8.15",
		1002003: "1.2.3",
		0:       "0.0.0",
	}
	for v, want := range cases {
		if got := formatLibvirtVersion(v); got != want {
			t.Fatalf("formatLibvirtVersion(%d) = %q, want %q", v, got, want)
		}
	}
}

func TestLibvirtTransport(t *testing.T) {
	// Explicit socket -> Unix transport, default URI.
	mode, addr, uri := libvirtTransport(Config{Socket: "/run/libvirt/libvirt-sock"})
	if mode != "socket" || addr != "/run/libvirt/libvirt-sock" || uri != "qemu:///system" {
		t.Fatalf("socket: mode=%q addr=%q uri=%q", mode, addr, uri)
	}

	// A host (no socket) -> TCP transport on the default port.
	mode, addr, uri = libvirtTransport(Config{Host: "10.0.0.4"})
	if mode != "tcp" || addr != "10.0.0.4:16509" || uri != "qemu:///system" {
		t.Fatalf("tcp: mode=%q addr=%q uri=%q", mode, addr, uri)
	}

	// An explicit port is honored.
	if _, addr, _ := libvirtTransport(Config{Host: "10.0.0.4", Port: 16510}); addr != "10.0.0.4:16510" {
		t.Fatalf("addr = %q, want 10.0.0.4:16510", addr)
	}

	// query overrides the connect URI; socket wins over host.
	mode, addr, uri = libvirtTransport(Config{Socket: "/s", Host: "10.0.0.4", Query: "lxc:///"})
	if mode != "socket" || addr != "/s" || uri != "lxc:///" {
		t.Fatalf("override: mode=%q addr=%q uri=%q", mode, addr, uri)
	}

	// Empty config (the builder injects the default socket before Probe, so this
	// path defaults to local TCP) — confirm the bare fallback.
	if mode, addr, _ := libvirtTransport(Config{}); mode != "tcp" || addr != "127.0.0.1:16509" {
		t.Fatalf("empty: mode=%q addr=%q", mode, addr)
	}
}
