package conn

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func libvirtTransport(cfg Config) (mode, addr, uri string) {
	return libvirtTransportWithTarget(newProbeTarget(cfg, defaultPortLibvirt))
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

func TestLibvirtDomainState(t *testing.T) {
	runMapCases(t, "libvirtDomainState", libvirtDomainState, map[int32]string{
		1: "running", 2: "blocked", 3: "paused", 4: "shutdown",
		5: "shutoff", 6: "crashed", 7: "pmsuspended", 0: "nostate", 99: "nostate",
	})
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

// TestLibvirtProbeReleasesHungDaemonConnection covers a libvirtd that accepts
// and never answers: the probe returns at its deadline and the client closes
// the connection instead of leaving go-libvirt waiting on it forever.
func TestLibvirtProbeReleasesHungDaemonConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "libvirt-sock")
	ln, err := net.Listen(networkUnix, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	closed := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			closed <- err
			return
		}
		defer func() { _ = c.Close() }()
		// Never reply; the read ends only when the client closes.
		_, err = io.Copy(io.Discard, c)
		closed <- err
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	probe, _ := Lookup(ProtocolNameLibvirt)
	if _, err := probe.Probe(ctx, Config{Socket: path}); err == nil {
		t.Fatal("probe of a silent daemon succeeded")
	}
	select {
	case err := <-closed:
		if err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("server side ended with %v, want client close", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("libvirt probe left the connection open after its deadline")
	}
}
