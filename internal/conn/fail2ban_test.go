package conn

import (
	"context"
	"net"
	"path/filepath"
	"testing"
)

func TestFail2banProbe(t *testing.T) {
	// A listening Unix socket stands in for a running fail2ban-server.
	sock := filepath.Join(t.TempDir(), "fail2ban.sock")
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
			_ = c.Close()
		}
	}()

	res, err := fail2banProtocol{}.Probe(context.Background(), Config{Socket: sock})
	if err != nil {
		t.Fatalf("probe a live socket: %v", err)
	}
	if res.Extra["socket"] != sock {
		t.Fatalf("extra = %v", res.Extra)
	}

	// A non-existent socket (no daemon) fails.
	if _, err := (fail2banProtocol{}).Probe(context.Background(), Config{Socket: filepath.Join(t.TempDir(), "absent.sock")}); err == nil {
		t.Fatal("a missing socket must fail the probe")
	}
}
