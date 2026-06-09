package conn

import (
	"bytes"
	"net"
	"testing"
)

func TestSSHRegistered(t *testing.T) {
	p, ok := Lookup("ssh")
	if !ok {
		t.Fatal("ssh not registered")
	}
	if p.DefaultPort() != 22 {
		t.Fatalf("default port = %d, want 22", p.DefaultPort())
	}
	if p.RequiresUser() {
		t.Fatal("ssh must not require a user (anonymous host-key check allowed)")
	}
}

func TestParseSSHBanner(t *testing.T) {
	proto, sw := parseSSHBanner("SSH-2.0-OpenSSH_9.6p1 Debian-1")
	if proto != "2.0" || sw != "OpenSSH_9.6p1 Debian-1" {
		t.Fatalf("parse = %q / %q", proto, sw)
	}
	if p, _ := parseSSHBanner("SSH-1.99-Server"); p != "1.99" {
		t.Fatalf("proto = %q", p)
	}
}

func TestSSHSucceeds(t *testing.T) {
	// Anonymous (no required auth): success iff the host key was captured.
	if !sshSucceeds(true, false, false) {
		t.Fatal("anonymous: host key captured + auth failed must succeed")
	}
	if sshSucceeds(false, false, false) {
		t.Fatal("no host key (kex failed) must not succeed")
	}
	// Credentialed: auth must succeed.
	if sshSucceeds(true, false, true) {
		t.Fatal("credentialed: auth failure must fail")
	}
	if !sshSucceeds(true, true, true) {
		t.Fatal("credentialed: host key + auth ok must succeed")
	}
}

func TestPrefixConnReplaysThenReadsConn(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		server.Write([]byte("CD"))
		server.Close()
	}()
	pc := &prefixConn{Conn: client, pre: bytes.NewReader([]byte("AB"))}

	buf := make([]byte, 2)
	n, _ := pc.Read(buf)
	if string(buf[:n]) != "AB" {
		t.Fatalf("first read = %q, want AB (the prefix)", buf[:n])
	}
	n, _ = pc.Read(buf)
	if string(buf[:n]) != "CD" {
		t.Fatalf("second read = %q, want CD (the conn)", buf[:n])
	}
}

func TestReadSSHBanner(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		// A pre-banner line (allowed by RFC 4253), then the SSH id, then kex bytes.
		server.Write([]byte("hello there\r\nSSH-2.0-OpenSSH_9.6\r\n"))
		server.Write([]byte{0x00, 0x01, 0x02}) // start of kex (must not be consumed)
	}()
	raw, banner, err := readSSHBanner(client)
	if err != nil {
		t.Fatal(err)
	}
	if banner != "SSH-2.0-OpenSSH_9.6" {
		t.Fatalf("banner = %q", banner)
	}
	if string(raw) != "hello there\r\nSSH-2.0-OpenSSH_9.6\r\n" {
		t.Fatalf("raw must include the pre-banner line for replay: %q", raw)
	}
}
