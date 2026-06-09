package conn

import (
	"bufio"
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
)

func TestNNTPRegistered(t *testing.T) {
	for _, name := range []string{"nntp", "nntps"} {
		p, ok := Lookup(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if p.DefaultPort() != 119 {
			t.Fatalf("%s default port = %d, want 119", name, p.DefaultPort())
		}
		if p.RequiresUser() {
			t.Fatalf("%s must not require a user", name)
		}
	}
}

// serveNNTP runs a fake NNTP server: it sends greeting, then answers each command
// from replies (keyed by the uppercased command, e.g. "AUTHINFO USER"). QUIT is
// always answered 205 and ends the session.
func serveNNTP(t *testing.T, greeting string, replies map[string]string) int {
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
		_, _ = io.WriteString(c, greeting)
		br := bufio.NewReader(c)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			f := strings.Fields(strings.TrimSpace(line))
			if len(f) == 0 {
				continue
			}
			key := strings.ToUpper(f[0])
			if key == "AUTHINFO" && len(f) > 1 {
				key += " " + strings.ToUpper(f[1])
			}
			if key == "QUIT" {
				_, _ = io.WriteString(c, "205 bye\r\n")
				return
			}
			if resp, ok := replies[key]; ok {
				_, _ = io.WriteString(c, resp)
			} else {
				_, _ = io.WriteString(c, "500 unknown command\r\n")
			}
		}
	}()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return port
}

func TestNNTPProbeAnonymous(t *testing.T) {
	port := serveNNTP(t, "200 Sermo News Server ready (posting ok)\r\n", nil)
	res, err := nntpProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["posting_allowed"] != "true" {
		t.Fatalf("posting_allowed = %q, want true", res.Extra["posting_allowed"])
	}
	if !strings.Contains(res.Extra["greeting"], "Sermo News") {
		t.Fatalf("greeting = %q", res.Extra["greeting"])
	}
}

func TestNNTPProbePostingProhibited(t *testing.T) {
	port := serveNNTP(t, "201 Sermo News Server ready (no posting)\r\n", nil)
	res, err := nntpProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["posting_allowed"] != "false" {
		t.Fatalf("posting_allowed = %q, want false", res.Extra["posting_allowed"])
	}
}

func TestNNTPProbeAuth(t *testing.T) {
	port := serveNNTP(t, "200 ready\r\n", map[string]string{
		"AUTHINFO USER": "381 password required\r\n",
		"AUTHINFO PASS": "281 authentication accepted\r\n",
	})
	res, err := nntpProtocol{}.Probe(context.Background(), Config{
		Host: "127.0.0.1", Port: port, User: "joe", Password: "secret",
	})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["posting_allowed"] != "true" {
		t.Fatalf("extra = %v", res.Extra)
	}
}

func TestNNTPProbeAuthRejected(t *testing.T) {
	port := serveNNTP(t, "200 ready\r\n", map[string]string{
		"AUTHINFO USER": "481 authentication rejected\r\n",
	})
	_, err := nntpProtocol{}.Probe(context.Background(), Config{
		Host: "127.0.0.1", Port: port, User: "joe", Password: "bad",
	})
	if err == nil {
		t.Fatal("a rejected authentication must error")
	}
}

func TestNNTPProbeBadGreeting(t *testing.T) {
	port := serveNNTP(t, "400 service temporarily unavailable\r\n", nil)
	_, err := nntpProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err == nil {
		t.Fatal("a 4xx greeting must error")
	}
}
