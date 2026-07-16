package conn

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestReadRESPRejectsUnsupportedType(t *testing.T) {
	// A RESP array (or any non-scalar type) must error, not return a payload with
	// its first byte stripped.
	br := bufio.NewReader(strings.NewReader("*2\r\n$3\r\nfoo\r\n$3\r\nbar\r\n"))
	if got, err := readRESP(br); err == nil {
		t.Fatalf("readRESP on array reply = %q, nil; want an error", got)
	}

	// Scalars still parse.
	br = bufio.NewReader(strings.NewReader("+PONG\r\n"))
	if got, err := readRESP(br); err != nil || got != "PONG" {
		t.Fatalf("readRESP(+PONG) = %q, %v; want PONG, nil", got, err)
	}
}

// rw pairs preloaded server replies (read side) with a capture buffer (write side).
type rw struct {
	in  *strings.Reader
	out *bytes.Buffer
}

func (r rw) Read(p []byte) (int, error)  { return r.in.Read(p) }
func (r rw) Write(p []byte) (int, error) { return r.out.Write(p) }

func infoBulk(body string) string { return fmt.Sprintf("$%d\r\n%s\r\n", len(body), body) }

func TestRedisHandshakeNoAuth(t *testing.T) {
	replies := "+PONG\r\n" + infoBulk("# Server\r\nredis_version:7.2.4\r\nredis_mode:standalone\r\n")
	conn := rw{in: strings.NewReader(replies), out: &bytes.Buffer{}}

	res, err := redisHandshake(conn, Config{})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if res.Version != "7.2.4" {
		t.Fatalf("version = %q, want 7.2.4", res.Version)
	}
	if strings.Contains(conn.out.String(), "AUTH") {
		t.Fatalf("no-auth handshake must not send AUTH: %q", conn.out.String())
	}
	if !strings.Contains(conn.out.String(), "PING") {
		t.Fatalf("handshake must PING: %q", conn.out.String())
	}
}

func TestRedisHandshakeExtraFields(t *testing.T) {
	info := "# Server\r\nredis_version:7.2.4\r\nuptime_in_seconds:3600\r\n" +
		"# Clients\r\nconnected_clients:12\r\n" +
		"# Memory\r\nused_memory:1048576\r\nmaxmemory:0\r\nmem_fragmentation_ratio:1.20\r\n" +
		"# Persistence\r\nloading:0\r\nrdb_last_bgsave_status:ok\r\naof_last_write_status:ok\r\n" +
		"# Replication\r\nrole:slave\r\nmaster_link_status:up\r\n"
	conn := rw{in: strings.NewReader("+PONG\r\n" + infoBulk(info)), out: &bytes.Buffer{}}

	res, err := redisHandshake(conn, Config{})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	want := map[string]string{
		"role": "slave", "master_link_status": "up", "connected_clients": "12",
		"used_memory": "1048576", "maxmemory": "0", "mem_fragmentation_ratio": "1.20",
		"rdb_last_bgsave_status": "ok", "aof_last_write_status": "ok", "loading": "0",
		"uptime_seconds": "3600",
	}
	for k, v := range want {
		if res.Extra[k] != v {
			t.Errorf("Extra[%q] = %q, want %q", k, res.Extra[k], v)
		}
	}
	if res.Version != "7.2.4" {
		t.Fatalf("version = %q, want 7.2.4", res.Version)
	}
}

func TestRedisHandshakeAuthUserAndPassword(t *testing.T) {
	replies := "+OK\r\n+PONG\r\n" + infoBulk("redis_version:7.0.0\r\n")
	conn := rw{in: strings.NewReader(replies), out: &bytes.Buffer{}}

	if _, err := redisHandshake(conn, Config{User: "monitor", Password: "secret"}); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	sent := conn.out.String()
	// AUTH monitor secret encoded as a RESP array.
	for _, want := range []string{"AUTH", "monitor", "secret"} {
		if !strings.Contains(sent, want) {
			t.Fatalf("sent %q missing %q", sent, want)
		}
	}
}

func TestRedisHandshakePasswordOnly(t *testing.T) {
	replies := "+OK\r\n+PONG\r\n" + infoBulk("redis_version:6.2.0\r\n")
	conn := rw{in: strings.NewReader(replies), out: &bytes.Buffer{}}
	if _, err := redisHandshake(conn, Config{Password: "only"}); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	sent := conn.out.String()
	if !strings.Contains(sent, "AUTH") || strings.Contains(sent, "default") {
		t.Fatalf("password-only AUTH should be 'AUTH only' (no username): %q", sent)
	}
}

func TestRedisHandshakeAuthError(t *testing.T) {
	assertHandshakeFails(t, redisHandshake, "-WRONGPASS invalid password\r\n", Config{Password: "bad"})
}

func TestRedisHandshakePingError(t *testing.T) {
	assertHandshakeFails(t, redisHandshake, "-LOADING server is loading\r\n", Config{})
}
