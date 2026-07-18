package conn

import (
	"strings"
	"testing"
)

func TestRcodeName(t *testing.T) {
	runMapCases(t, "rcodeName", rcodeName, map[int]string{
		0: "NOERROR", 1: "FORMERR", 2: "SERVFAIL",
		3: "NXDOMAIN", 4: "NOTIMP", 5: "REFUSED",
		9: "RCODE9",
	})
}

func TestIPPStatusName(t *testing.T) {
	runMapCases(t, "ippStatusName", ippStatusName, map[uint16]string{
		0x0000: "successful-ok",
		0x0401: "client-error-not-authorized",
		0x0406: "client-error-not-found",
		0x0500: "server-error-internal-error",
		0x0599: "0x0599",
	})
}

func TestMQTTConnackName(t *testing.T) {
	runMapCases(t, "mqttConnackName", mqttConnackName, map[byte]string{
		0: "accepted",
		1: "unacceptable-protocol-version",
		2: "identifier-rejected",
		3: "server-unavailable",
		4: "bad-username-or-password",
		5: "not-authorized",
		7: "code-7",
	})
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
