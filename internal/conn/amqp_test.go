package conn

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
)

// amqpShortStr encodes a 1-byte length-prefixed string (AMQP shortstr).
func amqpShortStr(s string) []byte { return append([]byte{byte(len(s))}, s...) }

// amqpLongStr encodes a 4-byte length-prefixed string (AMQP 'S' value, no tag).
func amqpLongStr(s string) []byte {
	b := make([]byte, 4+len(s))
	binary.BigEndian.PutUint32(b, uint32(len(s)))
	copy(b[4:], s)
	return b
}

// buildAMQPStart builds a Connection.Start method frame whose server-properties
// table carries the given string fields (all encoded as 'S' long strings).
func buildAMQPStart(props map[string]string) []byte {
	var table []byte
	for k, v := range props {
		table = append(table, amqpShortStr(k)...)
		table = append(table, 'S')
		table = append(table, amqpLongStr(v)...)
	}
	var payload []byte
	payload = binary.BigEndian.AppendUint16(payload, 10) // class: Connection
	payload = binary.BigEndian.AppendUint16(payload, 10) // method: Start
	payload = append(payload, 0, 9)                      // version-major, version-minor
	payload = binary.BigEndian.AppendUint32(payload, uint32(len(table)))
	payload = append(payload, table...)
	payload = append(payload, amqpLongStr("PLAIN AMQPLAIN")...) // mechanisms
	payload = append(payload, amqpLongStr("en_US")...)          // locales

	frame := []byte{1, 0, 0} // type=method, channel=0
	frame = binary.BigEndian.AppendUint32(frame, uint32(len(payload)))
	frame = append(frame, payload...)
	frame = append(frame, 0xCE) // frame-end
	return frame
}

// serveAMQP accepts one connection, drains the client's protocol header and
// replies with reply.
func serveAMQP(t *testing.T, reply []byte) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		var hdr [8]byte
		_, _ = io.ReadFull(c, hdr[:])
		_, _ = c.Write(reply)
	}()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return port
}

func TestAMQPProbeStart(t *testing.T) {
	frame := buildAMQPStart(map[string]string{
		"product":      "RabbitMQ",
		"version":      "3.13.7",
		"cluster_name": "rabbit@fr1",
		"platform":     "Erlang/OTP 26",
	})
	res, err := amqpProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: serveAMQP(t, frame)})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "3.13.7" {
		t.Fatalf("version = %q, want 3.13.7", res.Version)
	}
	if res.Extra["product"] != "RabbitMQ" {
		t.Fatalf("product = %q, want RabbitMQ", res.Extra["product"])
	}
	if res.Extra["cluster_name"] != "rabbit@fr1" {
		t.Fatalf("cluster_name = %q", res.Extra["cluster_name"])
	}
}

// A broker with no advertised properties is still a successful probe (the
// Connection.Start frame alone proves AMQP); version just stays empty.
func TestAMQPProbeNoProperties(t *testing.T) {
	res, err := amqpProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: serveAMQP(t, buildAMQPStart(nil))})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "" {
		t.Fatalf("version = %q, want empty", res.Version)
	}
}

// A version-negotiation rejection (server returns its own AMQP header) still
// proves an AMQP broker and must not error.
func TestAMQPProbeVersionMismatch(t *testing.T) {
	reply := []byte{'A', 'M', 'Q', 'P', 0, 0, 9, 1}
	res, err := amqpProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: serveAMQP(t, reply)})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["negotiation"] != "version-mismatch" {
		t.Fatalf("negotiation = %q", res.Extra["negotiation"])
	}
}

// A peer that is not an AMQP broker (wrong frame type) must fail the probe.
func TestAMQPProbeNotAMQP(t *testing.T) {
	reply := []byte{0x99, 0, 0, 0, 0, 0, 1, 0x00, 0xCE} // bogus frame type
	if _, err := (amqpProtocol{}).Probe(context.Background(), Config{Host: "127.0.0.1", Port: serveAMQP(t, reply)}); err == nil {
		t.Fatal("a non-method first frame must fail the probe")
	}
}

func TestParseAMQPTable(t *testing.T) {
	// Mixed types: a bool, a nested table (capabilities), then the strings we want.
	var table []byte
	table = append(table, amqpShortStr("flag")...)
	table = append(table, 't', 1)
	table = append(table, amqpShortStr("capabilities")...)
	table = append(table, 'F')
	table = append(table, amqpLongStr("")...) // empty nested table
	table = append(table, amqpShortStr("product")...)
	table = append(table, 'S')
	table = append(table, amqpLongStr("RabbitMQ")...)

	body := binary.BigEndian.AppendUint32(nil, uint32(len(table)))
	body = append(body, table...)

	got := parseAMQPTable(body)
	if got["product"] != "RabbitMQ" {
		t.Fatalf("product = %q, want RabbitMQ (skipping bool/table must keep alignment)", got["product"])
	}
}

// A method frame advertising a size beyond maxAMQPFrame must be rejected before
// the probe allocates the payload — the guard against a hostile/non-AMQP peer
// exhausting memory.
func TestAMQPProbeFrameTooLarge(t *testing.T) {
	hdr := []byte{1, 0, 0} // type=method, channel=0
	hdr = binary.BigEndian.AppendUint32(hdr, maxAMQPFrame+1)
	_, err := amqpProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: serveAMQP(t, hdr)})
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v, want a 'frame too large' rejection", err)
	}
}
