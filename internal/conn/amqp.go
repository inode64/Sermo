package conn

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
)

// Register amqp under its canonical name plus a "rabbitmq" alias, since RabbitMQ
// is the broker this probe is overwhelmingly used against.
func init() { Register(amqpProtocol{}, protocolAliasRabbitMQ) }

// amqpProtocol probes an AMQP 0-9-1 broker (RabbitMQ, …) natively — no external
// driver. It writes the AMQP protocol header and reads the broker's unprompted
// Connection.Start method, which the server sends before any authentication. A
// well-formed Connection.Start is proof the peer speaks AMQP, so the probe needs
// no credentials. The broker advertises its identity in the Start method's
// server-properties table; the probe extracts product/version/cluster_name from
// it (best effort), so `version` and an `expect: { product: ... }` assertion work
// like the other connection checks.
type amqpProtocol struct{}

func (amqpProtocol) Name() string       { return ProtocolNameAMQP }
func (amqpProtocol) DefaultPort() int   { return defaultPortAMQP }
func (amqpProtocol) RequiresUser() bool { return false }

// amqpHeader is the protocol header for AMQP 0-9-1: "AMQP" then 0, 0, 9, 1.
var amqpHeader = []byte{'A', 'M', 'Q', 'P', 0, 0, 9, 1}

// maxAMQPFrame bounds the Connection.Start payload we are willing to read, so a
// hostile or non-AMQP peer cannot make the probe allocate without limit. The
// real frame is a few hundred bytes.
const (
	maxAMQPFrame                   = 1 << 20
	amqpClassConnection            = 10
	amqpFrameEnd                   = 0xCE
	amqpFrameEndSize               = 1
	amqpFrameHeaderSize            = 7
	amqpFrameMethod                = 1
	amqpMethodHeaderSize           = 4
	amqpMethodStart                = 10
	amqpNegotiationVersionMismatch = "version-mismatch"
	amqpServerPropertiesOffset     = 6
)

const (
	amqpPropClusterName = "cluster_name"
	amqpPropPlatform    = "platform"
	amqpPropProduct     = "product"
	amqpPropVersion     = "version"
)

func (amqpProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	c, err := dialDeadline(ctx, cfg, defaultPortAMQP)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()

	if _, err := c.Write(amqpHeader); err != nil {
		return Result{}, err
	}

	// A method frame header is type(1), channel(2), size(4).
	var hdr [amqpFrameHeaderSize]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		return Result{}, err
	}

	// Version negotiation rejection: the broker replies with its own 8-byte
	// "AMQP" protocol header (offering a version it supports) and closes. That is
	// still proof of an AMQP broker, just one that declined 0-9-1.
	if hdr[0] == 'A' && hdr[1] == 'M' && hdr[2] == 'Q' && hdr[3] == 'P' {
		var rev [1]byte
		_, _ = io.ReadFull(c, rev[:]) // drain the 8th byte; ignore errors
		return Result{
			Version: fmt.Sprintf("AMQP %d-%d-%d", hdr[5], hdr[6], rev[0]),
			Extra:   map[string]string{extraNegotiation: amqpNegotiationVersionMismatch},
		}, nil
	}

	if hdr[0] != amqpFrameMethod {
		return Result{}, fmt.Errorf("unexpected AMQP frame type %d (want method)", hdr[0])
	}
	size := binary.BigEndian.Uint32(hdr[3:7])
	if size > maxAMQPFrame {
		return Result{}, fmt.Errorf("AMQP frame too large (%d bytes)", size)
	}

	// Read the payload plus the trailing frame-end octet (0xCE).
	payload := make([]byte, int(size)+amqpFrameEndSize)
	if _, err := io.ReadFull(c, payload); err != nil {
		return Result{}, err
	}
	if payload[size] != amqpFrameEnd {
		return Result{}, fmt.Errorf("malformed AMQP frame (bad frame-end 0x%02x)", payload[size])
	}
	body := payload[:size]

	// Method payload: class-id(2) method-id(2); Connection.Start is class 10,
	// method 10. version-major and version-minor follow, then server-properties.
	if len(body) < amqpMethodHeaderSize {
		return Result{}, fmt.Errorf("short AMQP method frame (%d bytes)", len(body))
	}
	if class, method := binary.BigEndian.Uint16(body[0:2]), binary.BigEndian.Uint16(body[2:4]); class != amqpClassConnection || method != amqpMethodStart {
		return Result{}, fmt.Errorf("unexpected AMQP method %d.%d (want Connection.Start 10.10)", class, method)
	}

	// Connect proven. Server identity (product/version/…) is best effort: a parse
	// failure leaves the fields empty without failing the liveness check, mirroring
	// redis INFO.
	res := Result{Extra: map[string]string{}}
	if len(body) >= amqpServerPropertiesOffset { // skip version-major(1) + version-minor(1)
		props := parseAMQPTable(body[amqpServerPropertiesOffset:])
		res.Version = props[amqpPropVersion]
		for _, k := range []string{amqpPropProduct, amqpPropClusterName, amqpPropPlatform} {
			if v := props[k]; v != "" {
				res.Extra[k] = v
			}
		}
	}
	return res, nil
}

// parseAMQPTable parses an AMQP 0-9-1 field table (a 4-byte big-endian byte
// length followed by name/value entries) and returns its long-string ('S')
// fields by name. Other field types are skipped by their encoded width so the
// cursor stays aligned; parsing stops at the first malformed entry, returning
// whatever was decoded so far (the caller treats extraction as best effort).
func parseAMQPTable(b []byte) map[string]string {
	out := map[string]string{}
	if len(b) < 4 {
		return out
	}
	n := binary.BigEndian.Uint32(b[0:4])
	b = b[4:]
	if uint32(len(b)) > n {
		b = b[:n] // ignore trailing bytes beyond the declared table length
	}
	for len(b) > 0 {
		nameLen := int(b[0])
		b = b[1:]
		if len(b) < nameLen+1 { // name + at least the value type tag
			break
		}
		name := string(b[:nameLen])
		typ := b[nameLen]
		b = b[nameLen+1:]
		s, isStr, used, ok := amqpFieldValue(typ, b)
		if !ok {
			break
		}
		if isStr {
			out[name] = s
		}
		b = b[used:]
	}
	return out
}

// amqpFieldValue decodes one AMQP field value of the given type from the front
// of b. It returns the decoded string (only for the string types 'S'/'x'), how
// many bytes the value consumed, and ok=false when the type is unknown or b is
// too short. Composite values ('A' array, 'F' table) are length-skipped rather
// than recursed into, since the probe only needs top-level string fields.
func amqpFieldValue(typ byte, b []byte) (s string, isStr bool, n int, ok bool) {
	fixed := func(width int) (string, bool, int, bool) {
		if len(b) < width {
			return "", false, 0, false
		}
		return "", false, width, true
	}
	lenPrefixed := func() (string, bool, int, bool) {
		if len(b) < 4 {
			return "", false, 0, false
		}
		size := int(binary.BigEndian.Uint32(b[0:4]))
		if size < 0 || len(b) < 4+size {
			return "", false, 0, false
		}
		return string(b[4 : 4+size]), true, 4 + size, true
	}
	switch typ {
	case 't', 'b', 'B': // bool, int8, uint8
		return fixed(1)
	case 's', 'u': // int16, uint16
		return fixed(2)
	case 'I', 'i', 'f': // int32, uint32, float
		return fixed(4)
	case 'l', 'd', 'T': // int64, double, timestamp
		return fixed(8)
	case 'D': // decimal: scale octet + int32
		return fixed(5)
	case 'V': // void
		return "", false, 0, true
	case 'S', 'x': // long string, byte array
		return lenPrefixed()
	case 'A', 'F': // field array, nested table — skip by length
		_, _, used, ok := lenPrefixed()
		return "", false, used, ok
	default:
		return "", false, 0, false
	}
}
