package conn

import (
	"strings"
	"testing"
	"time"
)

func TestParseProcUDP4Address(t *testing.T) {
	tests := []struct {
		in       string
		wantAddr string
		wantPort int
	}{
		{in: "00000000:0044", wantAddr: "0.0.0.0", wantPort: 68},
		{in: "0100007F:0035", wantAddr: "127.0.0.1", wantPort: 53},
	}
	for _, tt := range tests {
		addr, port, err := parseProcUDP4Address(tt.in)
		if err != nil {
			t.Fatalf("%s: %v", tt.in, err)
		}
		if addr != tt.wantAddr || port != tt.wantPort {
			t.Fatalf("%s = %s:%d, want %s:%d", tt.in, addr, port, tt.wantAddr, tt.wantPort)
		}
	}
}

func TestParseUDP4SocketTable(t *testing.T) {
	const table = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
 1576: 00000000:0044 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 37159 2 0000000000000000 0
 1577: 0100007F:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 37160 2 0000000000000000 0
`
	sock, ok, err := parseUDP4SocketTable(strings.NewReader(table), "0.0.0.0", 68)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected UDP/68 socket")
	}
	if sock.localAddress != "0.0.0.0" || sock.port != 68 || sock.state != "07" || sock.inode != "37159" {
		t.Fatalf("socket = %+v", sock)
	}

	if _, ok, err := parseUDP4SocketTable(strings.NewReader(table), "127.0.0.1", 68); err != nil || ok {
		t.Fatalf("localhost UDP/68 should be absent, ok=%v err=%v", ok, err)
	}
}

func TestParseDHClientLeases(t *testing.T) {
	const leases = `lease {
  interface "eth0";
  fixed-address 192.0.2.10;
  expire 4 2026/06/10 10:00:00;
}
lease {
  interface "eth0";
  fixed-address 192.0.2.11;
  expire 4 2026/06/12 10:00:00;
}
lease {
  interface "eth1";
  fixed-address 198.51.100.20;
  expire 4 2026/06/13 10:00:00;
}
`
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	lease, ok, err := parseDHClientLeases(strings.NewReader(leases), "eth0", now)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected an active eth0 lease")
	}
	if lease.interfaceName != "eth0" || lease.fixedAddress != "192.0.2.11" {
		t.Fatalf("lease = %+v", lease)
	}

	if _, ok, err := parseDHClientLeases(strings.NewReader(leases), "eth2", now); err != nil || ok {
		t.Fatalf("eth2 should have no active lease, ok=%v err=%v", ok, err)
	}
}
