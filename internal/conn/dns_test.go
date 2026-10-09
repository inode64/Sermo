package conn

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBuildDNSQueryHeader(t *testing.T) {
	q, err := buildDNSQuery(0xABCD, "example.com", 1)
	if err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint16(q[0:]) != 0xABCD {
		t.Fatalf("id = %x", q[0:2])
	}
	if binary.BigEndian.Uint16(q[2:]) != 0x0120 { // RD + AD set: a validating resolver reports AD only when asked
		t.Fatalf("flags = %x, want 0120", q[2:4])
	}
	if binary.BigEndian.Uint16(q[4:]) != 1 { // QDCOUNT
		t.Fatalf("qdcount = %d", binary.BigEndian.Uint16(q[4:]))
	}
	// trailing QTYPE=A(1), QCLASS=IN(1)
	if binary.BigEndian.Uint16(q[len(q)-4:]) != 1 || binary.BigEndian.Uint16(q[len(q)-2:]) != 1 {
		t.Fatalf("qtype/qclass wrong: %v", q[len(q)-4:])
	}
}

// dnsQNameBytes wire-encodes a domain name as length-prefixed labels ending in a
// zero byte, for crafting raw test messages.
func dnsQNameBytes(name string) []byte {
	var b []byte
	for label := range strings.SplitSeq(name, ".") {
		b = append(b, byte(len(label)))
		b = append(b, label...)
	}
	return append(b, 0)
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

func TestParseDNSReply(t *testing.T) {
	r, err := parseDNSReply(dnsResponse(0x1234, 0, 2))
	if err != nil {
		t.Fatal(err)
	}
	if r.id != 0x1234 || r.rcode != 0 || r.answers != 2 {
		t.Fatalf("parsed id=%x rcode=%d answers=%d", r.id, r.rcode, r.answers)
	}
	if r.authoritative || r.authenticated || r.truncated {
		t.Fatalf("flags set on a plain response: %+v", r)
	}
	// A query (QR=0) is not a valid response.
	q := make([]byte, 12)
	if _, err := parseDNSReply(q); err == nil {
		t.Fatal("QR=0 must be rejected")
	}
	// Too short.
	if _, err := parseDNSReply([]byte{0, 0}); err == nil {
		t.Fatal("short response must error")
	}
}

func TestParseDNSReplyHeaderFlags(t *testing.T) {
	msg := dnsResponse(0x1, 0, 0)
	msg[2] |= 0x04 | 0x02 // AA, TC
	msg[3] |= 0x20        // AD
	r, err := parseDNSReply(msg)
	if err != nil {
		t.Fatal(err)
	}
	if !r.authoritative || !r.authenticated || !r.truncated {
		t.Fatalf("flags = %+v, want aa, ad and tc", r)
	}
}

func TestBuildDNSQueryQType(t *testing.T) {
	q, err := buildDNSQuery(1, "example.com", dnsQTypeSOA)
	if err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint16(q[len(q)-4:]) != dnsQTypeSOA {
		t.Fatalf("qtype = %d, want SOA", binary.BigEndian.Uint16(q[len(q)-4:]))
	}
}

func TestParseDNSQType(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want uint16
		name string
	}{
		{in: "", want: dnsQTypeA, name: "A"},
		{in: "a", want: dnsQTypeA, name: "A"},
		{in: "AAAA", want: 28, name: "AAAA"},
		{in: "soa", want: dnsQTypeSOA, name: "SOA"},
		{in: "PTR", want: 12, name: "PTR"},
		{in: "mx", want: 15, name: "MX"},
		{in: "TXT", want: 16, name: "TXT"},
		{in: "ns", want: 2, name: "NS"},
		{in: "CNAME", want: 5, name: "CNAME"},
		{in: "SRV", want: 33, name: "SRV"},
	} {
		got, name, err := ParseDNSQType(tc.in)
		if err != nil || got != tc.want || name != tc.name {
			t.Fatalf("ParseDNSQType(%q) = %d, %q, %v; want %d, %q", tc.in, got, name, err, tc.want, tc.name)
		}
	}
	if _, _, err := ParseDNSQType("ANY"); err == nil {
		t.Fatal("an unsupported qtype must error")
	}
	if !strings.Contains(DNSQTypeSummary, "SOA") {
		t.Fatalf("summary %q must list the supported types", DNSQTypeSummary)
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

func TestParseDNSReplyAnswerAddrs(t *testing.T) {
	// Header (1 question, 3 answers) + question + A + CNAME (skipped) + AAAA.
	msg := dnsResponse(0x1, 0, 3)
	binary.BigEndian.PutUint16(msg[4:], 1) // QDCOUNT
	msg = append(msg, dnsQNameBytes("example.com")...)
	msg = append(msg, 0, 1, 0, 1) // QTYPE A, QCLASS IN
	msg = append(msg, dnsAnswerRR(1, []byte{93, 184, 216, 34})...)
	msg = append(msg, dnsAnswerRR(5, []byte{0xC0, 0x0C})...) // CNAME
	v6 := append([]byte{0x26, 0x06, 0x28, 0x00}, make([]byte, 12)...)
	msg = append(msg, dnsAnswerRR(28, v6)...)

	r, err := parseDNSReply(msg)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.addrs) != 2 || r.addrs[0] != "2606:2800::" || r.addrs[1] != "93.184.216.34" {
		t.Fatalf("addrs = %v, want the sorted A + AAAA records", r.addrs)
	}

	// A truncated answer section yields what was parsed, never panics.
	got, err := parseDNSReply(msg[:len(msg)-10])
	if err != nil {
		t.Fatal(err)
	}
	if len(got.addrs) != 1 {
		t.Fatalf("truncated = %v, want just the A record", got.addrs)
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

func TestDNSProbeInterfaceIgnoresLocalNameserver(t *testing.T) {
	oldAddrs := dnsInterfaceAddrs
	oldRouteAddrs := dnsRouteAddrs
	dnsInterfaceAddrs = func() ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{IP: net.ParseIP("192.168.2.254"), Mask: net.CIDRMask(24, 32)},
			&net.IPAddr{IP: net.ParseIP("2001:db8::53")},
		}, nil
	}
	dnsRouteAddrs = func(host string) (net.Addr, net.Addr, error) {
		if host == "192.168.5.254" {
			ip := net.ParseIP(host)
			return &net.UDPAddr{IP: ip, Port: 42300}, &net.UDPAddr{IP: ip, Port: 53}, nil
		}
		return &net.UDPAddr{IP: net.ParseIP("203.0.113.10"), Port: 42300}, &net.UDPAddr{IP: net.ParseIP(host), Port: 53}, nil
	}
	defer func() {
		dnsInterfaceAddrs = oldAddrs
		dnsRouteAddrs = oldRouteAddrs
	}()

	tests := []struct {
		name  string
		host  string
		iface string
		want  string
	}{
		{name: "ipv4 loopback", host: "127.0.0.1", iface: "br0"},
		{name: "ipv6 loopback", host: "::1", iface: "br0"},
		{name: "bracketed ipv6 loopback", host: "[::1]", iface: "br0"},
		{name: "local ipv4", host: "192.168.2.254", iface: "ppp0"},
		{name: "local ipv6", host: "2001:db8::53", iface: "ppp0"},
		{name: "bracketed local ipv6", host: "[2001:db8::53]", iface: "ppp0"},
		{name: "local by route", host: "192.168.5.254", iface: "ppp0"},
		{name: "remote nameserver", host: "192.0.2.53", iface: "br0", want: "br0"},
		{name: "empty interface", host: "192.0.2.53"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := dnsProbeInterface(tc.host, tc.iface); got != tc.want {
				t.Fatalf("dnsProbeInterface(%q, %q) = %q, want %q", tc.host, tc.iface, got, tc.want)
			}
		})
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

// dnsFakeServer answers every query with the response respond builds from the
// request bytes, and returns the server port.
func dnsFakeServer(t *testing.T, respond func(req []byte) []byte) int {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(respond(buf[:n]), addr)
		}
	}()
	_, port, err := net.SplitHostPort(pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(port)
	return n
}

func TestDNSProbeRefusedIsProtocolFailure(t *testing.T) {
	port := dnsFakeServer(t, func(req []byte) []byte {
		resp := dnsResponse(binary.BigEndian.Uint16(req), 5, 0) // REFUSED
		binary.BigEndian.PutUint16(resp[4:], 1)
		return append(resp, req[12:]...)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := dnsProtocol{}.Probe(ctx, Config{Host: "127.0.0.1", Port: port, Query: "example.com"})
	if err != nil {
		t.Fatalf("a REFUSED reply is a verdict, not a transport error: %v", err)
	}
	if !strings.Contains(res.Failure, "REFUSED") || !strings.Contains(res.Failure, "example.com") {
		t.Fatalf("Failure = %q, want the rcode and the name", res.Failure)
	}
	if res.Extra["rcode"] != "REFUSED" || res.Extra["answers"] != "0" || res.Extra["query"] != "example.com" {
		t.Fatalf("extra = %v, want structured evidence kept", res.Extra)
	}
}

func TestDNSProbeQTypeAndHeaderFlags(t *testing.T) {
	var gotQType uint16
	port := dnsFakeServer(t, func(req []byte) []byte {
		gotQType = binary.BigEndian.Uint16(req[len(req)-4:])
		resp := dnsResponse(binary.BigEndian.Uint16(req), 0, 0)
		resp[2] |= 0x04 // AA
		resp[3] |= 0x20 // AD
		binary.BigEndian.PutUint16(resp[4:], 1)
		return append(resp, req[12:]...)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := dnsProtocol{}.Probe(ctx, Config{Host: "127.0.0.1", Port: port, Query: "example.com", Params: map[string]string{ParamKeyQType: "soa"}})
	if err != nil || res.Failure != "" {
		t.Fatalf("Probe: %v %q", err, res.Failure)
	}
	if gotQType != dnsQTypeSOA {
		t.Fatalf("server saw qtype %d, want SOA", gotQType)
	}
	want := map[string]string{"qtype": "SOA", "aa": "true", "ad": "true", "tc": "false", "rcode": "NOERROR"}
	for k, v := range want {
		if res.Extra[k] != v {
			t.Fatalf("extra[%s] = %q, want %q (extra %v)", k, res.Extra[k], v, res.Extra)
		}
	}
	if _, err := (dnsProtocol{}).Probe(ctx, Config{Host: "127.0.0.1", Port: port, Params: map[string]string{ParamKeyQType: "ANY"}}); err == nil {
		t.Fatal("an unsupported qtype must fail before sending")
	}
}
