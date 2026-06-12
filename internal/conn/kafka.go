package conn

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
)

func init() { Register(kafkaProtocol{}) }

// kafkaProtocol probes a Kafka listener natively over the Kafka wire protocol —
// no external client or shell tool. It sends an ApiVersions request (API key 18,
// version 0), which a broker or a KRaft controller answers before any
// authentication, and reads the reply: a well-formed response whose correlation
// id matches the one we sent proves the peer speaks Kafka. The response lists
// every API the listener advertises, so the probe reports which role answered —
// a broker (data plane) listener exposes Produce, a KRaft controller listener
// exposes the Raft quorum APIs (Vote) — in Result.Extra, letting an expect: rule
// assert it reached the intended listener (broker vs controller).
type kafkaProtocol struct{}

func (kafkaProtocol) Name() string       { return "kafka" }
func (kafkaProtocol) DefaultPort() int   { return 9092 }
func (kafkaProtocol) RequiresUser() bool { return false }

const (
	kafkaAPIVersionsKey = 18         // ApiVersions request/response API key
	kafkaProduceKey     = 0          // Produce — served on a broker (data plane) listener
	kafkaVoteKey        = 52         // Vote — served on a KRaft controller (quorum) listener
	kafkaCorrelationID  = 0x5365726d // "Serm" — echoed back verbatim in the response header
	kafkaMinResponse    = 10         // correlation_id(4) + error_code(2) + array_len(4)
	maxKafkaResponse    = 1 << 20    // bound the reply so a non-Kafka peer cannot exhaust memory
)

// Probe opens the connection (TCP, TLS when configured) and runs the ApiVersions
// handshake. The caller's context bounds it.
func (kafkaProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	c, err := dialDeadline(ctx, cfg, 9092)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()

	if _, err := c.Write(kafkaAPIVersionsRequest()); err != nil {
		return Result{}, err
	}
	return readKafkaAPIVersions(c)
}

// kafkaAPIVersionsRequest builds a size-prefixed ApiVersions (v0) request with a
// request header v1 (api_key, api_version, correlation_id, client_id). The
// ApiVersions v0 body is empty, which keeps the request off the "flexible"
// (tagged-field) encoding that versions >= 3 require.
func kafkaAPIVersionsRequest() []byte {
	const clientID = "sermo"
	var body []byte
	body = binary.BigEndian.AppendUint16(body, kafkaAPIVersionsKey)   // api_key
	body = binary.BigEndian.AppendUint16(body, 0)                     // api_version
	body = binary.BigEndian.AppendUint32(body, kafkaCorrelationID)    // correlation_id
	body = binary.BigEndian.AppendUint16(body, uint16(len(clientID))) // client_id length
	body = append(body, clientID...)
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(body))), body...)
}

// readKafkaAPIVersions reads the size-prefixed response, verifies the echoed
// correlation id, and parses the ApiVersions v0 body: error_code (int16) then an
// int32-counted array of {api_key, min_version, max_version} (each int16). The
// error code is exposed rather than failed on, mirroring redis INFO: any
// well-formed, correlation-matched reply proves the listener speaks Kafka (a
// broker that does not support v0 still answers in v0 form with error_code 35).
func readKafkaAPIVersions(r io.Reader) (Result, error) {
	var sizeBuf [4]byte
	if _, err := io.ReadFull(r, sizeBuf[:]); err != nil {
		return Result{}, err
	}
	size := binary.BigEndian.Uint32(sizeBuf[:])
	if size < kafkaMinResponse || size > maxKafkaResponse {
		return Result{}, fmt.Errorf("kafka: implausible response size %d", size)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return Result{}, err
	}

	// Response header v0 is a single int32 correlation id; a mismatch means the
	// peer is not answering our ApiVersions request (i.e. not a Kafka listener).
	if corr := binary.BigEndian.Uint32(buf[0:4]); corr != kafkaCorrelationID {
		return Result{}, fmt.Errorf("kafka: correlation id mismatch (got %#x) — peer does not speak Kafka", corr)
	}
	body := buf[4:]

	errorCode := binary.BigEndian.Uint16(body[0:2])
	count := binary.BigEndian.Uint32(body[2:6])
	body = body[6:]
	// Each array entry is 6 bytes; reject a count the remaining payload cannot
	// hold so a hostile length cannot drive an out-of-bounds read.
	if uint64(count)*6 > uint64(len(body)) {
		return Result{}, fmt.Errorf("kafka: ApiVersions count %d exceeds payload (%d bytes)", count, len(body))
	}
	keys := make(map[uint16]bool, count)
	for i := uint32(0); i < count; i++ {
		off := i * 6
		keys[binary.BigEndian.Uint16(body[off:off+2])] = true
	}

	res := Result{Extra: map[string]string{
		"api_count":   strconv.Itoa(int(count)),
		"error_code":  strconv.Itoa(int(errorCode)),
		"produce_api": yesNo(keys[kafkaProduceKey]),
		"vote_api":    yesNo(keys[kafkaVoteKey]),
	}}
	// Best-effort role label from the advertised APIs: a broker (data plane)
	// listener exposes Produce; a KRaft controller listener exposes the Raft
	// quorum APIs (Vote) and not Produce.
	switch {
	case keys[kafkaProduceKey]:
		res.Extra["role"] = "broker"
	case keys[kafkaVoteKey]:
		res.Extra["role"] = "controller"
	}
	return res, nil
}

// yesNo renders a boolean as the "yes"/"no" string the expect: comparison uses
// for the role-detection flags.
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
