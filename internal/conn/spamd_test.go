package conn

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
)

func TestParseSpamdPong(t *testing.T) {
	if v, ok := parseSpamdPong("SPAMD/1.5 0 PONG"); !ok || v != "1.5" {
		t.Fatalf("got %q/%v, want 1.5/true", v, ok)
	}
	if _, ok := parseSpamdPong("SPAMD/1.5 0 EX_OK"); ok {
		t.Fatal("a non-PONG reply must be rejected")
	}
	if _, ok := parseSpamdPong("HTTP/1.1 200 OK"); ok {
		t.Fatal("a non-SPAMD reply must be rejected")
	}
}

func TestSpamdProbeAgainstFakeServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var gotPing string
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		line, _ := bufio.NewReader(c).ReadString('\n')
		gotPing = line
		_, _ = c.Write([]byte("SPAMD/1.5 0 PONG\r\n"))
	}()

	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	res, err := spamdProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["ping"] != "pong" || res.Extra["protocol"] != "1.5" {
		t.Fatalf("extra = %v", res.Extra)
	}
	if !strings.HasPrefix(gotPing, "PING SPAMC/") {
		t.Fatalf("server received %q, want a PING", gotPing)
	}
}
