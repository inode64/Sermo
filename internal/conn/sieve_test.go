package conn

import (
	"context"
	"net"
	"strings"
	"testing"
)

func TestSieveImplementation(t *testing.T) {
	v, ok := sieveImplementation(`"IMPLEMENTATION" "Dovecot Pro v2.3"`)
	if !ok || v != "Dovecot Pro v2.3" {
		t.Fatalf("got %q/%v, want Dovecot Pro v2.3/true", v, ok)
	}
	if _, ok := sieveImplementation(`"SIEVE" "fileinto reject"`); ok {
		t.Fatal("a non-IMPLEMENTATION capability must not match")
	}
}

func serveSieve(t *testing.T, lines []string) int {
	t.Helper()
	return serveOnce(t, func(c net.Conn) {
		for _, l := range lines {
			_, _ = c.Write([]byte(l + "\r\n"))
		}
	})
}

func TestSieveProbeGreetingOK(t *testing.T) {
	port := serveSieve(t, []string{
		`"IMPLEMENTATION" "Dovecot Pro v2.3.19"`,
		`"SIEVE" "fileinto reject envelope"`,
		`"SASL" "PLAIN LOGIN"`,
		`OK "ManageSieve ready."`,
	})
	res, err := sieveProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "Dovecot Pro v2.3.19" {
		t.Fatalf("version = %q", res.Version)
	}
	if res.Extra["implementation"] != "Dovecot Pro v2.3.19" {
		t.Fatalf("extra = %v", res.Extra)
	}
}

// A BYE or NO instead of the greeting is the server refusing the session: busy.
func TestSieveProbeRefused(t *testing.T) {
	for _, reply := range []string{`BYE "Too many connections"`, `NO (TRYLATER) "Server shutting down"`} {
		port := serveSieve(t, []string{reply})
		res, err := (sieveProtocol{}).Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
		assertBusy(t, res, err, strings.Fields(reply)[0])
	}
	port := serveSieve(t, []string{`NO "Internal error"`})
	if _, err := (sieveProtocol{}).Probe(context.Background(), Config{Host: "127.0.0.1", Port: port}); err == nil {
		t.Fatal("a refusal that is not temporary must stay a failure")
	}
}
