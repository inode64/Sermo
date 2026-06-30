package conn

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"testing"
)

// buildKafkaAPIVersions builds a size-prefixed ApiVersions v0 response carrying
// the given error code and the given supported API keys (min/max left at 0).
func buildKafkaAPIVersions(errorCode uint16, apiKeys ...uint16) []byte {
	var body []byte
	body = binary.BigEndian.AppendUint32(body, kafkaCorrelationID)
	body = binary.BigEndian.AppendUint16(body, errorCode)
	body = binary.BigEndian.AppendUint32(body, uint32(len(apiKeys)))
	for _, k := range apiKeys {
		body = binary.BigEndian.AppendUint16(body, k) // api_key
		body = binary.BigEndian.AppendUint16(body, 0) // min_version
		body = binary.BigEndian.AppendUint16(body, 0) // max_version
	}
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(body))), body...)
}

// serveKafka accepts one connection, drains the client's size-prefixed
// ApiVersions request and replies with reply.
func serveKafka(t *testing.T, reply []byte) int {
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
		var sz [4]byte
		if _, err := io.ReadFull(c, sz[:]); err != nil {
			return
		}
		_, _ = io.CopyN(io.Discard, c, int64(binary.BigEndian.Uint32(sz[:])))
		_, _ = c.Write(reply)
	}()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return port
}

func TestKafkaProbeBroker(t *testing.T) {
	// A broker listener advertises Produce (key 0) plus assorted others.
	reply := buildKafkaAPIVersions(0, kafkaProduceKey, 1, 3, kafkaAPIVersionsKey)
	res, err := kafkaProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: serveKafka(t, reply)})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["role"] != "broker" {
		t.Fatalf("role = %q, want broker", res.Extra["role"])
	}
	if res.Extra["produce_api"] != "yes" {
		t.Fatalf("produce_api = %q, want yes", res.Extra["produce_api"])
	}
	if res.Extra["error_code"] != "0" {
		t.Fatalf("error_code = %q, want 0", res.Extra["error_code"])
	}
}

func TestKafkaProbeController(t *testing.T) {
	// A KRaft controller listener advertises the Vote quorum API but not Produce.
	reply := buildKafkaAPIVersions(0, kafkaVoteKey, 53, 55, kafkaAPIVersionsKey)
	res, err := kafkaProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: serveKafka(t, reply)})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["role"] != "controller" {
		t.Fatalf("role = %q, want controller", res.Extra["role"])
	}
	if res.Extra["vote_api"] != "yes" {
		t.Fatalf("vote_api = %q, want yes", res.Extra["vote_api"])
	}
	if res.Extra["produce_api"] != "no" {
		t.Fatalf("produce_api = %q, want no", res.Extra["produce_api"])
	}
}

// An ApiVersions reply with an error code (e.g. UNSUPPORTED_VERSION) still proves
// the listener speaks Kafka: the probe succeeds and surfaces the code.
func TestKafkaProbeErrorCodeStillAlive(t *testing.T) {
	reply := buildKafkaAPIVersions(35, kafkaProduceKey)
	res, err := kafkaProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: serveKafka(t, reply)})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["error_code"] != "35" {
		t.Fatalf("error_code = %q, want 35", res.Extra["error_code"])
	}
}

// A peer that echoes the wrong correlation id is not a Kafka listener answering
// our request and must fail the probe.
func TestKafkaProbeCorrelationMismatch(t *testing.T) {
	var body []byte
	body = binary.BigEndian.AppendUint32(body, 0xdeadbeef) // wrong correlation id
	body = binary.BigEndian.AppendUint16(body, 0)
	body = binary.BigEndian.AppendUint32(body, 0)
	reply := append(binary.BigEndian.AppendUint32(nil, uint32(len(body))), body...)
	if _, err := (kafkaProtocol{}).Probe(context.Background(), Config{Host: "127.0.0.1", Port: serveKafka(t, reply)}); err == nil {
		t.Fatal("a correlation-id mismatch must fail the probe")
	}
}

// A truncated reply (too small to hold the ApiVersions header) must fail.
func TestKafkaProbeShortResponse(t *testing.T) {
	reply := append(binary.BigEndian.AppendUint32(nil, 4), 0, 0, 0, 0) // size=4, only a correlation id
	if _, err := (kafkaProtocol{}).Probe(context.Background(), Config{Host: "127.0.0.1", Port: serveKafka(t, reply)}); err == nil {
		t.Fatal("a short response must fail the probe")
	}
}

// A count that overruns the payload must be rejected, not read out of bounds.
func TestKafkaProbeBogusCount(t *testing.T) {
	var body []byte
	body = binary.BigEndian.AppendUint32(body, kafkaCorrelationID)
	body = binary.BigEndian.AppendUint16(body, 0)
	body = binary.BigEndian.AppendUint32(body, 1000) // claims 1000 entries, sends none
	reply := append(binary.BigEndian.AppendUint32(nil, uint32(len(body))), body...)
	if _, err := (kafkaProtocol{}).Probe(context.Background(), Config{Host: "127.0.0.1", Port: serveKafka(t, reply)}); err == nil {
		t.Fatal("an array count exceeding the payload must fail the probe")
	}
}
