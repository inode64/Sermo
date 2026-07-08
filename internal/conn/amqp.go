package conn

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"

	"sermo/internal/units"
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

// maxAMQPFrame bounds the Connection.Start payload we are willing to read, so a
// hostile or non-AMQP peer cannot make the probe allocate without limit. The
// real frame is a few hundred bytes.
const (
	maxAMQPFrame                   = units.BytesPerMiB
	amqpClassConnection            = 10
	amqpFrameEnd                   = 0xCE
	amqpFrameEndSize               = 1
	amqpFrameHeaderSize            = 7
	amqpFrameMethod                = 1
	amqpFrameTypeOffset            = 0
	amqpFramePayloadSizeStart      = 3
	amqpFramePayloadSizeEnd        = 7
	amqpMethodHeaderSize           = 4
	amqpMethodClassStart           = 0
	amqpMethodClassEnd             = 2
	amqpMethodIDStart              = 2
	amqpMethodIDEnd                = 4
	amqpMethodStart                = 10
	amqpNegotiationVersionMismatch = "version-mismatch"
	amqpServerPropertiesOffset     = 6
	amqpVersionMajorOffset         = 5
	amqpVersionMinorOffset         = 6
	amqpRevisionOffset             = 0
	amqpRevisionBytes              = 1
	amqpProtocolID                 = 0
	amqpProtocolMajor091           = 0
	amqpProtocolMinor091           = 9
	amqpProtocolRevision091        = 1
)

const (
	amqpTableLengthBytes      = 4
	amqpFieldNameLengthBytes  = 1
	amqpFieldTypeTagBytes     = 1
	amqpFieldNameLengthOffset = 0
	amqpLongStringLengthBytes = 4
	amqpFieldWidthOctet       = 1
	amqpFieldWidthShort       = 2
	amqpFieldWidthLong        = 4
	amqpFieldWidthLongLong    = 8
	amqpFieldWidthDecimal     = 5
)

const (
	amqpFieldTypeBool        = 't'
	amqpFieldTypeInt8        = 'b'
	amqpFieldTypeUint8       = 'B'
	amqpFieldTypeInt16       = 's'
	amqpFieldTypeUint16      = 'u'
	amqpFieldTypeInt32       = 'I'
	amqpFieldTypeUint32      = 'i'
	amqpFieldTypeFloat       = 'f'
	amqpFieldTypeInt64       = 'l'
	amqpFieldTypeDouble      = 'd'
	amqpFieldTypeTimestamp   = 'T'
	amqpFieldTypeDecimal     = 'D'
	amqpFieldTypeVoid        = 'V'
	amqpFieldTypeLongString  = 'S'
	amqpFieldTypeByteArray   = 'x'
	amqpFieldTypeArray       = 'A'
	amqpFieldTypeNestedTable = 'F'
)

const (
	amqpSignature       = "AMQP"
	amqpVersionFormat   = "AMQP %d-%d-%d"
	amqpPropClusterName = "cluster_name"
	amqpPropPlatform    = "platform"
	amqpPropProduct     = "product"
	amqpPropVersion     = "version"
)

// amqpHeader is the protocol header for AMQP 0-9-1: "AMQP" then 0, 0, 9, 1.
var amqpHeader = append([]byte(amqpSignature),
	amqpProtocolID,
	amqpProtocolMajor091,
	amqpProtocolMinor091,
	amqpProtocolRevision091,
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
	if string(hdr[:len(amqpSignature)]) == amqpSignature {
		var rev [amqpRevisionBytes]byte
		_, _ = io.ReadFull(c, rev[:]) // drain the 8th byte; ignore errors
		return Result{
			Version: fmt.Sprintf(amqpVersionFormat, hdr[amqpVersionMajorOffset], hdr[amqpVersionMinorOffset], rev[amqpRevisionOffset]),
			Extra:   map[string]string{extraNegotiation: amqpNegotiationVersionMismatch},
		}, nil
	}

	if hdr[amqpFrameTypeOffset] != amqpFrameMethod {
		return Result{}, fmt.Errorf("unexpected AMQP frame type %d (want method)", hdr[amqpFrameTypeOffset])
	}
	size := binary.BigEndian.Uint32(hdr[amqpFramePayloadSizeStart:amqpFramePayloadSizeEnd])
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
	if class, method := binary.BigEndian.Uint16(body[amqpMethodClassStart:amqpMethodClassEnd]), binary.BigEndian.Uint16(body[amqpMethodIDStart:amqpMethodIDEnd]); class != amqpClassConnection || method != amqpMethodStart {
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
	if len(b) < amqpTableLengthBytes {
		return out
	}
	n := binary.BigEndian.Uint32(b[:amqpTableLengthBytes])
	b = b[amqpTableLengthBytes:]
	if uint32(len(b)) > n {
		b = b[:n] // ignore trailing bytes beyond the declared table length
	}
	for len(b) > 0 {
		nameLen := int(b[amqpFieldNameLengthOffset])
		b = b[amqpFieldNameLengthBytes:]
		if len(b) < nameLen+amqpFieldTypeTagBytes {
			break
		}
		name := string(b[:nameLen])
		typ := b[nameLen]
		b = b[nameLen+amqpFieldTypeTagBytes:]
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
		if len(b) < amqpLongStringLengthBytes {
			return "", false, 0, false
		}
		size := int(binary.BigEndian.Uint32(b[:amqpLongStringLengthBytes]))
		if size < 0 || len(b) < amqpLongStringLengthBytes+size {
			return "", false, 0, false
		}
		return string(b[amqpLongStringLengthBytes : amqpLongStringLengthBytes+size]), true, amqpLongStringLengthBytes + size, true
	}
	switch typ {
	case amqpFieldTypeBool, amqpFieldTypeInt8, amqpFieldTypeUint8:
		return fixed(amqpFieldWidthOctet)
	case amqpFieldTypeInt16, amqpFieldTypeUint16:
		return fixed(amqpFieldWidthShort)
	case amqpFieldTypeInt32, amqpFieldTypeUint32, amqpFieldTypeFloat:
		return fixed(amqpFieldWidthLong)
	case amqpFieldTypeInt64, amqpFieldTypeDouble, amqpFieldTypeTimestamp:
		return fixed(amqpFieldWidthLongLong)
	case amqpFieldTypeDecimal:
		return fixed(amqpFieldWidthDecimal)
	case amqpFieldTypeVoid:
		return "", false, 0, true
	case amqpFieldTypeLongString, amqpFieldTypeByteArray:
		return lenPrefixed()
	case amqpFieldTypeArray, amqpFieldTypeNestedTable:
		_, _, used, ok := lenPrefixed()
		return "", false, used, ok
	default:
		return "", false, 0, false
	}
}
