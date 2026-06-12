package conn

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
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

// dnsAnswerRR appends one answer RR using a compression pointer to offset 12
// (the question name), as real servers do.
func dnsAnswerRR(typ uint16, rdata []byte) []byte {
	rr := []byte{0xC0, 0x0C} // name: pointer to the question
	tail := make([]byte, 10)
	binary.BigEndian.PutUint16(tail[0:], typ)
	binary.BigEndian.PutUint16(tail[2:], 1) // class IN
	binary.BigEndian.PutUint16(tail[8:], uint16(len(rdata)))
	return append(append(rr, tail...), rdata...)
}

func TestParseDNSAnswerAddrs(t *testing.T) {
	// Header (1 question, 3 answers) + question + A + CNAME (skipped) + AAAA.
	msg := dnsResponse(0x1, 0, 3)
	binary.BigEndian.PutUint16(msg[4:], 1) // QDCOUNT
	q, err := encodeDNSName("example.com")
	if err != nil {
		t.Fatal(err)
	}
	msg = append(msg, q...)
	msg = append(msg, 0, 1, 0, 1) // QTYPE A, QCLASS IN
	msg = append(msg, dnsAnswerRR(1, []byte{93, 184, 216, 34})...)
	msg = append(msg, dnsAnswerRR(5, []byte{0xC0, 0x0C})...) // CNAME
	v6 := append([]byte{0x26, 0x06, 0x28, 0x00}, make([]byte, 12)...)
	msg = append(msg, dnsAnswerRR(28, v6)...)

	addrs := parseDNSAnswerAddrs(msg)
	if len(addrs) != 2 || addrs[0] != "2606:2800::" || addrs[1] != "93.184.216.34" {
		t.Fatalf("addrs = %v, want the sorted A + AAAA records", addrs)
	}

	// A truncated answer section yields what was parsed, never panics.
	if got := parseDNSAnswerAddrs(msg[:len(msg)-10]); len(got) != 1 {
		t.Fatalf("truncated = %v, want just the A record", got)
	}
}

func TestFirstNameserver(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resolv.conf")
	body := "# comment\nsearch lan\nnameserver 10.64.0.1\nnameserver 10.64.0.2\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ns, err := firstNameserver(path)
	if err != nil || ns != "10.64.0.1" {
		t.Fatalf("firstNameserver = %q, %v; want 10.64.0.1", ns, err)
	}
	if err := os.WriteFile(path, []byte("search lan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := firstNameserver(path); err == nil {
		t.Fatal("a resolv.conf without nameserver entries must error")
	}
}

func TestDNSProbeResolvconf(t *testing.T) {
	// A fake DNS server answering one A record, reached via resolvconf: true.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pc.Close() }()
	go func() {
		buf := make([]byte, 1500)
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		resp := dnsResponse(binary.BigEndian.Uint16(buf[:n]), 0, 1)
		binary.BigEndian.PutUint16(resp[4:], 1)
		resp = append(resp, buf[12:n]...) // echo the question
		resp = append(resp, dnsAnswerRR(1, []byte{203, 0, 113, 7})...)
		_, _ = pc.WriteTo(resp, addr)
	}()

	host, port, err := net.SplitHostPort(pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	rc := filepath.Join(dir, "resolv.conf")
	if err := os.WriteFile(rc, []byte("nameserver "+host+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := resolvConfPath
	resolvConfPath = rc
	defer func() { resolvConfPath = old }()

	n, _ := strconv.Atoi(port)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := dnsProtocol{}.Probe(ctx, Config{Port: n, Query: "example.com", Params: map[string]string{"resolvconf": "true"}})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.Extra["addresses"] != "203.0.113.7" || res.Extra["rcode"] != "NOERROR" {
		t.Fatalf("extra = %v, want the resolved address", res.Extra)
	}
}
