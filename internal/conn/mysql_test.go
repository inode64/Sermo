package conn

import (
	"context"
	"io"
	"net"
	"strconv"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestMySQLNoUserOptional(t *testing.T) {
	// mysql must NOT require a user: a credential-free greeting probe is allowed.
	p, ok := Lookup("mysql")
	if !ok {
		t.Fatal("mysql not registered")
	}
	if p.RequiresUser() {
		t.Fatal("mysql must not require a user (greeting liveness)")
	}
}

// buildMySQLHandshake builds an Initial Handshake Packet (protocol 10) whose
// server_version is the given string.
func buildMySQLHandshake(version string) []byte {
	payload := []byte{0x0a}
	payload = append(payload, version...)
	payload = append(payload, 0)               // null terminator
	payload = append(payload, 0, 0, 0, 0)      // connection id (unused by the probe)
	hdr := []byte{byte(len(payload)), 0, 0, 0} // 3-byte LE length + seq id
	return append(hdr, payload...)
}

// serveMySQL accepts one connection and writes reply (the server speaks first).
func serveMySQL(t *testing.T, reply []byte) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
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
		_, _ = c.Write(reply)
		_, _ = io.Copy(io.Discard, c)
	}()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return port
}

func TestMySQLGreeting(t *testing.T) {
	res, err := mysqlProtocol{}.Probe(context.Background(),
		Config{Host: "127.0.0.1", Port: serveMySQL(t, buildMySQLHandshake("11.4.2-MariaDB"))})
	if err != nil {
		t.Fatalf("greeting probe: %v", err)
	}
	if res.Version != "11.4.2-MariaDB" {
		t.Fatalf("version = %q, want 11.4.2-MariaDB", res.Version)
	}
}

func TestMySQLGreetingErrPacket(t *testing.T) {
	// 0xff ERR packet: code(2, LE) + message. The server is up but refusing.
	payload := append([]byte{0xff, 0x10, 0x04}, "Host blocked"...)
	reply := append([]byte{byte(len(payload)), 0, 0, 0}, payload...)
	if _, err := (mysqlProtocol{}).Probe(context.Background(),
		Config{Host: "127.0.0.1", Port: serveMySQL(t, reply)}); err == nil {
		t.Fatal("an ERR handshake must fail the probe")
	}
}

func TestMySQLGreetingNotMySQL(t *testing.T) {
	// A peer whose first packet is not a protocol-10 handshake must fail.
	reply := []byte{0x02, 0, 0, 0, 0x99, 0x00}
	if _, err := (mysqlProtocol{}).Probe(context.Background(),
		Config{Host: "127.0.0.1", Port: serveMySQL(t, reply)}); err == nil {
		t.Fatal("a non-handshake first packet must fail the probe")
	}
}

func TestBuildDSN(t *testing.T) {
	dsn := buildDSN(Config{
		Host: "db.example", Port: 3307, User: "monitor",
		Password: "p@ss:w/rd", Database: "app", TLS: "skip-verify",
	})
	c, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("ParseDSN(%q): %v", dsn, err)
	}
	if c.User != "monitor" || c.Passwd != "p@ss:w/rd" {
		t.Fatalf("creds = %q/%q", c.User, c.Passwd)
	}
	if c.Net != "tcp" || c.Addr != "db.example:3307" {
		t.Fatalf("addr = %s %q", c.Net, c.Addr)
	}
	if c.DBName != "app" {
		t.Fatalf("db = %q", c.DBName)
	}
	if c.TLSConfig != "skip-verify" {
		t.Fatalf("tls = %q, want skip-verify", c.TLSConfig)
	}
}

func TestBuildDSNDefaultsAndPlaintext(t *testing.T) {
	dsn := buildDSN(Config{User: "u"})
	c, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != "127.0.0.1:3306" {
		t.Fatalf("addr = %q, want default 127.0.0.1:3306", c.Addr)
	}
	if c.TLSConfig != "" {
		t.Fatalf("tls = %q, want empty (plaintext) by default", c.TLSConfig)
	}
}

func TestNormalizeTLS(t *testing.T) {
	cases := map[string]string{
		"": "", "false": "", "no": "",
		"true": "true", "yes": "true",
		"skip-verify": "skip-verify",
		"custom":      "custom",
	}
	for in, want := range cases {
		if got := normalizeTLS(in); got != want {
			t.Errorf("normalizeTLS(%q) = %q, want %q", in, got, want)
		}
	}
}
