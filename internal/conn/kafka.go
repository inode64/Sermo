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

func (kafkaProtocol) Name() string       { return ProtocolNameKafka }
func (kafkaProtocol) DefaultPort() int   { return defaultPortKafka }
func (kafkaProtocol) RequiresUser() bool { return false }

const (
	kafkaAPIVersionsKey = 18         // ApiVersions request/response API key
	kafkaProduceKey     = 0          // Produce — served on a broker (data plane) listener
	kafkaVoteKey        = 52         // Vote — served on a KRaft controller (quorum) listener
	kafkaCorrelationID  = 0x5365726d // "Serm" — echoed back verbatim in the response header
	kafkaAPIVersion     = 0
	kafkaMinResponse    = 10      // correlation_id(4) + error_code(2) + array_len(4)
	maxKafkaResponse    = 1 << 20 // bound the reply so a non-Kafka peer cannot exhaust memory
	kafkaRoleBroker     = "broker"
	kafkaRoleController = "controller"
	kafkaFlagYes        = "yes"
	kafkaFlagNo         = "no"
	kafkaClientID       = "sermo"
)

const (
	kafkaSizePrefixBytes        = 4
	kafkaCorrelationIDStart     = 0
	kafkaCorrelationIDEnd       = 4
	kafkaResponseBodyOffset     = kafkaCorrelationIDEnd
	kafkaErrorCodeStart         = 0
	kafkaErrorCodeEnd           = 2
	kafkaAPICountStart          = 2
	kafkaAPICountEnd            = 6
	kafkaAPIVersionsHeaderBytes = kafkaAPICountEnd
	kafkaAPIVersionEntryBytes   = 6
	kafkaAPIKeyStart            = 0
	kafkaAPIKeyEnd              = 2
)

// Probe opens the connection (TCP, TLS when configured) and runs the ApiVersions
// handshake. The caller's context bounds it.
func (kafkaProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	c, err := dialDeadline(ctx, cfg, defaultPortKafka)
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
	var body []byte
	body = binary.BigEndian.AppendUint16(body, kafkaAPIVersionsKey)        // api_key
	body = binary.BigEndian.AppendUint16(body, kafkaAPIVersion)            // api_version
	body = binary.BigEndian.AppendUint32(body, kafkaCorrelationID)         // correlation_id
	body = binary.BigEndian.AppendUint16(body, uint16(len(kafkaClientID))) // client_id length
	body = append(body, kafkaClientID...)
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(body))), body...)
}

// readKafkaAPIVersions reads the size-prefixed response, verifies the echoed
// correlation id, and parses the ApiVersions v0 body: error_code (int16) then an
// int32-counted array of {api_key, min_version, max_version} (each int16). The
// error code is exposed rather than failed on, mirroring redis INFO: any
// well-formed, correlation-matched reply proves the listener speaks Kafka (a
// broker that does not support v0 still answers in v0 form with error_code 35).
func readKafkaAPIVersions(r io.Reader) (Result, error) {
	var sizeBuf [kafkaSizePrefixBytes]byte
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
	if corr := binary.BigEndian.Uint32(buf[kafkaCorrelationIDStart:kafkaCorrelationIDEnd]); corr != kafkaCorrelationID {
		return Result{}, fmt.Errorf("kafka: correlation id mismatch (got %#x) — peer does not speak Kafka", corr)
	}
	body := buf[kafkaResponseBodyOffset:]

	errorCode := binary.BigEndian.Uint16(body[kafkaErrorCodeStart:kafkaErrorCodeEnd])
	count := binary.BigEndian.Uint32(body[kafkaAPICountStart:kafkaAPICountEnd])
	body = body[kafkaAPIVersionsHeaderBytes:]
	// Each array entry is 6 bytes; reject a count the remaining payload cannot
	// hold so a hostile length cannot drive an out-of-bounds read.
	if uint64(count)*kafkaAPIVersionEntryBytes > uint64(len(body)) {
		return Result{}, fmt.Errorf("kafka: ApiVersions count %d exceeds payload (%d bytes)", count, len(body))
	}
	keys := make(map[uint16]bool, count)
	for i := uint32(0); i < count; i++ {
		off := i * kafkaAPIVersionEntryBytes
		keys[binary.BigEndian.Uint16(body[off+kafkaAPIKeyStart:off+kafkaAPIKeyEnd])] = true
	}

	res := Result{Extra: map[string]string{
		ExtraKeyKafkaAPICount:   strconv.Itoa(int(count)),
		ExtraKeyKafkaErrorCode:  strconv.Itoa(int(errorCode)),
		ExtraKeyKafkaProduceAPI: yesNo(keys[kafkaProduceKey]),
		ExtraKeyKafkaVoteAPI:    yesNo(keys[kafkaVoteKey]),
	}}
	// Best-effort role label from the advertised APIs: a broker (data plane)
	// listener exposes Produce; a KRaft controller listener exposes the Raft
	// quorum APIs (Vote) and not Produce.
	switch {
	case keys[kafkaProduceKey]:
		res.Extra[ExtraKeyRole] = kafkaRoleBroker
	case keys[kafkaVoteKey]:
		res.Extra[ExtraKeyRole] = kafkaRoleController
	}
	return res, nil
}

// yesNo renders a boolean as the "yes"/"no" string the expect: comparison uses
// for the role-detection flags.
func yesNo(b bool) string {
	if b {
		return kafkaFlagYes
	}
	return kafkaFlagNo
}
