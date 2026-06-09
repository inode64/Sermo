package conn

import (
	"encoding/binary"
	"testing"
)

func TestDNSRegistered(t *testing.T) {
	p, ok := Lookup("dns")
	if !ok {
		t.Fatal("dns not registered")
	}
	if p.DefaultPort() != 53 {
		t.Fatalf("default port = %d, want 53", p.DefaultPort())
	}
	if p.RequiresUser() {
		t.Fatal("dns must not require a user")
	}
}

func TestEncodeDNSName(t *testing.T) {
	got, err := encodeDNSName("www.example.com")
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{3, 'w', 'w', 'w', 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0}
	if string(got) != string(want) {
		t.Fatalf("encodeDNSName = %v, want %v", got, want)
	}
	if _, err := encodeDNSName("a." + string(make([]byte, 64)) + ".com"); err == nil {
		t.Fatal("an over-long label must error")
	}
}

func TestBuildDNSQueryHeader(t *testing.T) {
	q, err := buildDNSQuery(0xABCD, "example.com", 1)
	if err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint16(q[0:]) != 0xABCD {
		t.Fatalf("id = %x", q[0:2])
	}
	if binary.BigEndian.Uint16(q[2:]) != 0x0100 { // RD set
		t.Fatalf("flags = %x, want 0100", q[2:4])
	}
	if binary.BigEndian.Uint16(q[4:]) != 1 { // QDCOUNT
		t.Fatalf("qdcount = %d", binary.BigEndian.Uint16(q[4:]))
	}
	// trailing QTYPE=A(1), QCLASS=IN(1)
	if binary.BigEndian.Uint16(q[len(q)-4:]) != 1 || binary.BigEndian.Uint16(q[len(q)-2:]) != 1 {
		t.Fatalf("qtype/qclass wrong: %v", q[len(q)-4:])
	}
}

// dnsResponse crafts a minimal DNS response header.
func dnsResponse(id uint16, rcode, ancount int) []byte {
	b := make([]byte, 12)
	binary.BigEndian.PutUint16(b[0:], id)
	b[2] = 0x81              // QR=1, RD=1
	b[3] = byte(rcode) & 0xf // RA + rcode
	binary.BigEndian.PutUint16(b[6:], uint16(ancount))
	return b
}

func TestParseDNSResponse(t *testing.T) {
	id, rcode, answers, err := parseDNSResponse(dnsResponse(0x1234, 0, 2))
	if err != nil {
		t.Fatal(err)
	}
	if id != 0x1234 || rcode != 0 || answers != 2 {
		t.Fatalf("parsed id=%x rcode=%d answers=%d", id, rcode, answers)
	}
	// A query (QR=0) is not a valid response.
	q := make([]byte, 12)
	if _, _, _, err := parseDNSResponse(q); err == nil {
		t.Fatal("QR=0 must be rejected")
	}
	// Too short.
	if _, _, _, err := parseDNSResponse([]byte{0, 0}); err == nil {
		t.Fatal("short response must error")
	}
}

func TestDNSOK(t *testing.T) {
	if !dnsResponseOK(0) || !dnsResponseOK(3) { // NOERROR, NXDOMAIN
		t.Fatal("NOERROR and NXDOMAIN must count as the server answering")
	}
	if dnsResponseOK(2) || dnsResponseOK(5) { // SERVFAIL, REFUSED
		t.Fatal("SERVFAIL/REFUSED must not count as healthy")
	}
}
