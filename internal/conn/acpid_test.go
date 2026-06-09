package conn

import (
	"context"
	"net"
	"path/filepath"
	"testing"
)

func TestAcpidRegistered(t *testing.T) {
	p, ok := Lookup("acpid")
	if !ok {
		t.Fatal("acpid not registered")
	}
	if p.DefaultPort() != 0 {
		t.Fatalf("default port = %d, want 0 (socket-only)", p.DefaultPort())
	}
	if p.RequiresUser() {
		t.Fatal("acpid must not require a user")
	}
}

func TestAcpidProbe(t *testing.T) {
	// A listening Unix socket stands in for a running acpid: connect succeeds.
	sock := filepath.Join(t.TempDir(), "acpid.socket")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close() // acpid would keep it open for events; we only test connect
		}
	}()

	res, err := acpidProtocol{}.Probe(context.Background(), Config{Socket: sock})
	if err != nil {
		t.Fatalf("probe a live socket: %v", err)
	}
	if res.Extra["socket"] != sock {
		t.Fatalf("extra = %v", res.Extra)
	}

	// A non-existent socket (no daemon) fails.
	if _, err := (acpidProtocol{}).Probe(context.Background(), Config{Socket: filepath.Join(t.TempDir(), "absent.socket")}); err == nil {
		t.Fatal("a missing socket must fail the probe")
	}
}
