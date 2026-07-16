package conn

import (
	"context"
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
	var gotPing string
	port := serveBanner(t, "SPAMD/1.5 0 PONG\r\n", &gotPing)
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
