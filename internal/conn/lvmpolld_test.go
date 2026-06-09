package conn

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

func TestLvmpolldRegistered(t *testing.T) {
	p, ok := Lookup("lvmpolld")
	if !ok {
		t.Fatal("lvmpolld not registered")
	}
	if p.DefaultPort() != 0 {
		t.Fatalf("lvmpolld default port = %d, want 0 (socket-only)", p.DefaultPort())
	}
	if p.RequiresUser() {
		t.Fatal("lvmpolld must not require a user")
	}
}

func TestParseLVMDaemonReply(t *testing.T) {
	f := parseLVMDaemonReply("response = \"OK\"\nprotocol = \"lvmpolld\"\nversion = 1\n")
	if f["response"] != "OK" || f["protocol"] != "lvmpolld" || f["version"] != "1" {
		t.Fatalf("parsed = %v", f)
	}
}

// serveLVMDaemon answers one libdaemon hello request on a Unix socket: it reads
// until the "\n##\n" delimiter and writes reply framed the same way.
func serveLVMDaemon(t *testing.T, reply string) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "lvmpolld.socket")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		buf := make([]byte, 0, 128)
		tmp := make([]byte, 64)
		for !strings.Contains(string(buf), "\n##\n") {
			n, rerr := c.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
			}
			if rerr != nil {
				return
			}
		}
		if !strings.Contains(string(buf), "hello") {
			return
		}
		_, _ = c.Write([]byte(reply + "\n##\n"))
	}()
	return sock
}

func TestLvmpolldProbeHello(t *testing.T) {
	sock := serveLVMDaemon(t, "response = \"OK\"\nprotocol = \"lvmpolld\"\nversion = 1\n")
	res, err := lvmpolldProtocol{}.Probe(context.Background(), Config{Socket: sock})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "" {
		t.Fatalf("version = %q, want empty (no software version in handshake)", res.Version)
	}
	if res.Extra["protocol"] != "lvmpolld" || res.Extra["protocol_version"] != "1" {
		t.Fatalf("extra = %v", res.Extra)
	}
	if res.Extra["socket"] != sock {
		t.Fatalf("socket = %q", res.Extra["socket"])
	}
}

func TestLvmpolldProbeWrongDaemon(t *testing.T) {
	sock := serveLVMDaemon(t, "response = \"OK\"\nprotocol = \"lvmetad\"\nversion = 3\n")
	if _, err := (lvmpolldProtocol{}).Probe(context.Background(), Config{Socket: sock}); err == nil {
		t.Fatal("a different LVM daemon must be rejected")
	}
}

func TestLvmpolldProbeNotOK(t *testing.T) {
	sock := serveLVMDaemon(t, "response = \"failed\"\nreason = \"busy\"\n")
	if _, err := (lvmpolldProtocol{}).Probe(context.Background(), Config{Socket: sock}); err == nil {
		t.Fatal("a non-OK response must error")
	}
}
