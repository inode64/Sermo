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
		"# Memory\r\nused_memory:1048576\r\nmaxmemory:0\r\nmaxmemory_policy:noeviction\r\nmem_fragmentation_ratio:1.20\r\n" +
		"# Stats\r\nevicted_keys:7\r\nsync_full:2\r\n" +
		"# Persistence\r\nloading:0\r\nrdb_last_bgsave_status:ok\r\naof_last_write_status:ok\r\n" +
		"# Replication\r\nrole:slave\r\nmaster_link_status:up\r\n"
	conn := rw{in: strings.NewReader("+PONG\r\n" + infoBulk(info)), out: &bytes.Buffer{}}

	res, err := redisHandshake(conn, Config{})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	want := map[string]string{
		"role": "slave", "master_link_status": "up", ExtraKeyConnectedClients: "12",
		"used_memory": "1048576", "maxmemory": "0", "mem_fragmentation_ratio": "1.20",
		"rdb_last_bgsave_status": "ok", "aof_last_write_status": "ok", "loading": "0",
		"uptime_seconds": "3600", "maxmemory_policy": "noeviction", "evicted_keys": "7",
		"sync_full": "2",
		// No dbN line and no maxmemory limit: both derived fields are 0, not missing.
		"keys": "0", "maxmemory_used_pct": "0",
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

func TestReadRESPBoundsBulkReplies(t *testing.T) {
	for _, tt := range []struct {
		name, wire string
		wantLen    int
		wantErr    bool
	}{
		{"empty", "$0\r\n\r\n", 0, false},
		{"null", "$-1\r\n", 0, false},
		{"invalid negative", "$-2\r\n", 0, true},
		{"huge", "$9999999999999\r\n", 0, true},
		{"overflow", "$999999999999999999999999\r\n", 0, true},
		{"above limit", fmt.Sprintf("$%d\r\n", maxRedisBulk+1), 0, true},
		{"at limit", infoBulk(strings.Repeat("x", maxRedisBulk)), maxRedisBulk, false},
		{"truncated", "$3\r\nx", 0, true},
		{"bad terminator", "$1\r\nx!!", 0, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readRESP(bufio.NewReader(strings.NewReader(tt.wire)))
			if (err != nil) != tt.wantErr || (!tt.wantErr && len(got) != tt.wantLen) {
				t.Fatalf("reply length=%d err=%v", len(got), err)
			}
		})
	}
}

func TestRedisHandshakeDerivedFields(t *testing.T) {
	info := "# Memory\r\nused_memory:966367642\r\nmaxmemory:1073741824\r\n" +
		"# Keyspace\r\ndb0:keys=144074,expires=144070,avg_ttl=359349753\r\ndb3:keys=26,expires=0,avg_ttl=0\r\n" +
		"dbsize_hint:keys=999\r\n"
	conn := rw{in: strings.NewReader("+PONG\r\n" + infoBulk(info)), out: &bytes.Buffer{}}

	res, err := redisHandshake(conn, Config{})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if got := res.Extra["keys"]; got != "144100" {
		t.Errorf("keys = %q, want the sum over dbN lines only (144100)", got)
	}
	if got := res.Extra["maxmemory_used_pct"]; got != "90.00" {
		t.Errorf("maxmemory_used_pct = %q, want 90.00", got)
	}
}

func TestRedisMaxMemoryUsedPctUnavailable(t *testing.T) {
	for name, fields := range map[string]map[string]string{
		"no maxmemory field": {"used_memory": "10"},
		"bad used_memory":    {"used_memory": "x", "maxmemory": "100"},
	} {
		if pct, ok := redisMaxMemoryUsedPct(fields); ok {
			t.Errorf("%s: got %q, want unavailable", name, pct)
		}
	}
}

func TestRedisHandshakeRejectedCalls(t *testing.T) {
	info := "# Server\r\nredis_version:7.2.4\r\n# Commandstats\r\n" +
		"cmdstat_set:calls=900,usec=4500,usec_per_call=5.00,rejected_calls=12,failed_calls=0\r\n" +
		"cmdstat_get:calls=1200,usec=2400,usec_per_call=2.00,rejected_calls=117,failed_calls=3\r\n"
	conn := rw{in: strings.NewReader("+PONG\r\n" + infoBulk(info)), out: &bytes.Buffer{}}

	res, err := redisHandshake(conn, Config{})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if got := res.Extra["rejected_calls"]; got != "129" || res.Version != "7.2.4" {
		t.Errorf("rejected_calls = %q version = %q, want the sum over every command (129) from the same reply", got, res.Version)
	}
}

func TestRedisRejectedCallsMissingOnOldServers(t *testing.T) {
	fields := parseRedisInfo("cmdstat_get:calls=10,usec=20,usec_per_call=2.00\r\n")
	if total, ok := redisRejectedCalls(fields); ok {
		t.Fatalf("a server without the counter reported %d", total)
	}
}

// The health fields and the command stats come from one INFO all: one round
// trip per probe.
func TestRedisHandshakeSendsOneInfoAll(t *testing.T) {
	conn := rw{in: strings.NewReader("+PONG\r\n" + infoBulk("redis_version:7.2.4\r\n")), out: &bytes.Buffer{}}
	if _, err := redisHandshake(conn, Config{}); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	sent := conn.out.String()
	if strings.Count(sent, redisCommandInfo) != 1 || !strings.Contains(sent, "$3\r\nall\r\n") {
		t.Fatalf("want exactly one INFO all, sent %q", sent)
	}
}
