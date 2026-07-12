package conn

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRcodeName(t *testing.T) {
	cases := map[int]string{
		0: "NOERROR", 1: "FORMERR", 2: "SERVFAIL",
		3: "NXDOMAIN", 4: "NOTIMP", 5: "REFUSED",
		9: "RCODE9",
	}
	for code, want := range cases {
		if got := rcodeName(code); got != want {
			t.Errorf("rcodeName(%d) = %q, want %q", code, got, want)
		}
	}
}

func TestIPPStatusName(t *testing.T) {
	cases := map[uint16]string{
		0x0000: "successful-ok",
		0x0401: "client-error-not-authorized",
		0x0406: "client-error-not-found",
		0x0500: "server-error-internal-error",
		0x0599: "0x0599",
	}
	for code, want := range cases {
		if got := ippStatusName(code); got != want {
			t.Errorf("ippStatusName(%#04x) = %q, want %q", code, got, want)
		}
	}
}

func TestMQTTConnackName(t *testing.T) {
	cases := map[byte]string{
		0: "accepted",
		1: "unacceptable-protocol-version",
		2: "identifier-rejected",
		3: "server-unavailable",
		4: "bad-username-or-password",
		5: "not-authorized",
		7: "code-7",
	}
	for code, want := range cases {
		if got := mqttConnackName(code); got != want {
			t.Errorf("mqttConnackName(%d) = %q, want %q", code, got, want)
		}
	}
}

func TestMySQLDSNDefaultsAndEscaping(t *testing.T) {
	dsn := MySQLDSN(Config{User: "mon", Password: "p@ss/word"})
	if !strings.Contains(dsn, "tcp(127.0.0.1:3306)") {
		t.Fatalf("dsn lacks default host/port: %q", dsn)
	}
	if !strings.HasPrefix(dsn, "mon:") {
		t.Fatalf("dsn lacks user: %q", dsn)
	}

	custom := MySQLDSN(Config{Host: "db.internal", Port: 3307, User: "mon"})
	if !strings.Contains(custom, "tcp(db.internal:3307)") {
		t.Fatalf("dsn lacks explicit host/port: %q", custom)
	}
}

func TestDNSAndDHCPIDs(t *testing.T) {
	// Random IDs: just require a few draws not to be all identical, so a
	// stuck-at-zero regression cannot pass.
	seenDNS := map[uint16]bool{}
	seenDHCP := map[uint32]bool{}
	for range 8 {
		seenDNS[dnsID()] = true
		seenDHCP[randXID32()] = true
	}
	if len(seenDNS) < 2 || len(seenDHCP) < 2 {
		t.Fatalf("ids look constant: dns %v dhcp %v", seenDNS, seenDHCP)
	}
}

func TestLibvirtTimeout(t *testing.T) {
	if d := libvirtTimeout(context.Background()); d != 10*time.Second {
		t.Fatalf("no deadline: %v, want 10s", d)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if d := libvirtTimeout(ctx); d <= 0 || d > time.Minute {
		t.Fatalf("future deadline: %v", d)
	}
	past, cancel2 := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel2()
	if d := libvirtTimeout(past); d != time.Nanosecond {
		t.Fatalf("past deadline: %v, want 1ns fail-fast", d)
	}
}
