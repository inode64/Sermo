package conn

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

func init() { Register(dnsProtocol{}) }

// dnsProtocol probes a DNS server natively: it sends an A query (over UDP) for a
// configurable name (default "localhost") and verifies the server answers. A
// NOERROR or NXDOMAIN reply means the server is up and speaking DNS; SERVFAIL,
// REFUSED, a transport error or a timeout fail the check. No authentication.
type dnsProtocol struct{}

func (dnsProtocol) Name() string       { return "dns" }
func (dnsProtocol) DefaultPort() int   { return 53 }
func (dnsProtocol) RequiresUser() bool { return false }

func (dnsProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 53
	}
	name := cfg.Query
	if name == "" {
		name = "localhost"
	}

	id := dnsID()
	query, err := buildDNSQuery(id, name, 1) // QTYPE A
	if err != nil {
		return Result{}, err
	}

	c, err := (&net.Dialer{}).DialContext(ctx, "udp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}

	if _, err := c.Write(query); err != nil {
		return Result{}, err
	}
	buf := make([]byte, 1500)
	n, err := c.Read(buf)
	if err != nil {
		return Result{}, err
	}
	rid, rcode, answers, err := parseDNSResponse(buf[:n])
	if err != nil {
		return Result{}, err
	}
	if rid != id {
		return Result{}, errors.New("DNS response id mismatch")
	}
	if !dnsResponseOK(rcode) {
		return Result{}, fmt.Errorf("DNS query for %q returned %s", name, rcodeName(rcode))
	}
	return Result{Extra: map[string]string{
		"query":   name,
		"rcode":   rcodeName(rcode),
		"answers": strconv.Itoa(answers),
	}}, nil
}

// dnsResponseOK reports whether an rcode means the server answered healthily: a
// successful lookup (NOERROR) or an authoritative "no such name" (NXDOMAIN).
func dnsResponseOK(rcode int) bool { return rcode == 0 || rcode == 3 }

func rcodeName(rcode int) string {
	switch rcode {
	case 0:
		return "NOERROR"
	case 1:
		return "FORMERR"
	case 2:
		return "SERVFAIL"
	case 3:
		return "NXDOMAIN"
	case 4:
		return "NOTIMP"
	case 5:
		return "REFUSED"
	default:
		return "RCODE" + strconv.Itoa(rcode)
	}
}

func dnsID() uint16 {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0x1234
	}
	return binary.BigEndian.Uint16(b[:])
}

// buildDNSQuery builds a standard recursive query message (header + one question).
func buildDNSQuery(id uint16, name string, qtype uint16) ([]byte, error) {
	qname, err := encodeDNSName(name)
	if err != nil {
		return nil, err
	}
	msg := make([]byte, 12)
	binary.BigEndian.PutUint16(msg[0:], id)
	binary.BigEndian.PutUint16(msg[2:], 0x0100) // flags: RD=1
	binary.BigEndian.PutUint16(msg[4:], 1)      // QDCOUNT=1
	msg = append(msg, qname...)
	tail := make([]byte, 4)
	binary.BigEndian.PutUint16(tail[0:], qtype) // QTYPE
	binary.BigEndian.PutUint16(tail[2:], 1)     // QCLASS=IN
	return append(msg, tail...), nil
}

// encodeDNSName encodes a domain name as length-prefixed labels ending with a
// zero byte.
func encodeDNSName(name string) ([]byte, error) {
	var b bytes.Buffer
	name = strings.TrimSuffix(name, ".")
	if name != "" {
		for _, label := range strings.Split(name, ".") {
			if len(label) == 0 || len(label) > 63 {
				return nil, fmt.Errorf("invalid DNS label %q", label)
			}
			b.WriteByte(byte(len(label)))
			b.WriteString(label)
		}
	}
	b.WriteByte(0)
	return b.Bytes(), nil
}

// parseDNSResponse reads a DNS message header, returning the id, RCODE and answer
// count. It errors on a too-short message or a query (QR=0).
func parseDNSResponse(b []byte) (id uint16, rcode int, answers int, err error) {
	if len(b) < 12 {
		return 0, 0, 0, errors.New("short DNS response")
	}
	id = binary.BigEndian.Uint16(b[0:])
	if b[2]&0x80 == 0 {
		return id, 0, 0, errors.New("not a DNS response (QR=0)")
	}
	rcode = int(b[3] & 0x0f)
	answers = int(binary.BigEndian.Uint16(b[6:]))
	return id, rcode, answers, nil
}
