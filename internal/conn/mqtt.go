package conn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
)

func init() { Register(mqttProtocol{}) }

const (
	mqttClientID                          = "sermo-check"
	mqttProtocolName                      = "MQTT"
	mqttConnackNameAccepted               = "accepted"
	mqttConnackNameBadCredentials         = "bad-username-or-password"
	mqttConnackNameIDRejected             = "identifier-rejected"
	mqttConnackNameNotAuthorized          = "not-authorized"
	mqttConnackNameServerUnavailable      = "server-unavailable"
	mqttConnackNameUnacceptableProto      = "unacceptable-protocol-version"
	mqttConnackCodeAccepted          byte = 0
	mqttConnackCodeUnacceptableProto byte = 1
	mqttConnackCodeIDRejected        byte = 2
	mqttConnackCodeServerUnavailable byte = 3
	mqttConnackCodeBadCredentials    byte = 4
	mqttConnackCodeNotAuthorized     byte = 5
	mqttConnackPacketType            byte = 0x20
	mqttConnackSessionPresent        byte = 0x01
	mqttConnectCleanSession          byte = 0x02
	mqttConnectPacketType            byte = 0x10
	mqttConnectPasswordFlag          byte = 0x40
	mqttConnectUserFlag              byte = 0x80
	mqttProtocolLevel311             byte = 0x04
	mqttRemainingLengthBase               = 128
	mqttRemainingLengthMoreFlag      byte = 0x80
	mqttStringLengthShift                 = 8
	mqttKeepAliveSeconds                  = 60
)

const (
	mqttPacketTypeOffset         = 0
	mqttConnackMinBytes          = 4
	mqttConnackSessionFlagOffset = 2
	mqttConnackReturnCodeOffset  = 3
)

// mqttProtocol probes an MQTT broker natively (MQTT 3.1.1): it performs the
// CONNECT handshake and verifies the broker answers with a CONNACK accepting the
// connection (return code 0). With no credentials it is an anonymous connect;
// `user`/`password` authenticate. `tls` enables MQTTS (port 8883). A CONNACK is
// proof the broker speaks MQTT; a non-zero return code (refused) fails the check
// with the reason.
type mqttProtocol struct{}

func (mqttProtocol) Name() string       { return ProtocolNameMQTT }
func (mqttProtocol) DefaultPort() int   { return defaultPortMQTT }
func (mqttProtocol) RequiresUser() bool { return false }

func (mqttProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	c, err := dialDeadline(ctx, cfg, defaultPortMQTT)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()

	if _, err := c.Write(buildMQTTConnect(mqttClientID, cfg.User, cfg.Password)); err != nil {
		return Result{}, err
	}
	var ack [mqttConnackMinBytes]byte
	if _, err := io.ReadFull(c, ack[:]); err != nil {
		return Result{}, err
	}
	code, sessionPresent, err := parseMQTTConnack(ack[:])
	if err != nil {
		return Result{}, err
	}
	if code != mqttConnackCodeAccepted {
		return Result{}, fmt.Errorf("MQTT connection refused: %s", mqttConnackName(code))
	}
	extra := map[string]string{extraConnack: mqttConnackName(code)}
	if sessionPresent {
		extra[extraSession] = strconv.FormatBool(true)
	}
	return Result{Extra: extra}, nil
}

// buildMQTTConnect builds an MQTT 3.1.1 CONNECT packet with a clean session and
// optional username/password.
func buildMQTTConnect(clientID, user, pass string) []byte {
	var vh bytes.Buffer
	writeMQTTString(&vh, mqttProtocolName)
	vh.WriteByte(mqttProtocolLevel311)
	flags := mqttConnectCleanSession
	if user != "" {
		flags |= mqttConnectUserFlag
	}
	if pass != "" {
		flags |= mqttConnectPasswordFlag
	}
	vh.WriteByte(flags)
	vh.WriteByte(byte(mqttKeepAliveSeconds >> mqttStringLengthShift))
	vh.WriteByte(byte(mqttKeepAliveSeconds))
	writeMQTTString(&vh, clientID)
	if user != "" {
		writeMQTTString(&vh, user)
	}
	if pass != "" {
		writeMQTTString(&vh, pass)
	}

	var pkt bytes.Buffer
	pkt.WriteByte(mqttConnectPacketType)
	writeMQTTRemainingLength(&pkt, vh.Len())
	pkt.Write(vh.Bytes())
	return pkt.Bytes()
}

// writeMQTTString writes a 2-byte big-endian length-prefixed UTF-8 string.
func writeMQTTString(b *bytes.Buffer, s string) {
	n := len(s)
	b.WriteByte(byte(n >> mqttStringLengthShift))
	b.WriteByte(byte(n))
	b.WriteString(s)
}

// writeMQTTRemainingLength writes the MQTT variable-length "remaining length".
func writeMQTTRemainingLength(b *bytes.Buffer, n int) {
	for {
		d := byte(n % mqttRemainingLengthBase)
		n /= mqttRemainingLengthBase
		if n > 0 {
			d |= mqttRemainingLengthMoreFlag
		}
		b.WriteByte(d)
		if n == 0 {
			break
		}
	}
}

// parseMQTTConnack reads a CONNACK packet: the connect return code and the
// session-present flag.
func parseMQTTConnack(b []byte) (code byte, sessionPresent bool, err error) {
	if len(b) < mqttConnackMinBytes {
		return 0, false, errors.New("short MQTT CONNACK")
	}
	if b[mqttPacketTypeOffset] != mqttConnackPacketType {
		return 0, false, fmt.Errorf("not an MQTT CONNACK (0x%02x)", b[mqttPacketTypeOffset])
	}
	return b[mqttConnackReturnCodeOffset], b[mqttConnackSessionFlagOffset]&mqttConnackSessionPresent != 0, nil
}

// mqttConnackNames names each CONNACK return code (MQTT 3.1.1).
var mqttConnackNames = map[byte]string{
	mqttConnackCodeAccepted:          mqttConnackNameAccepted,
	mqttConnackCodeUnacceptableProto: mqttConnackNameUnacceptableProto,
	mqttConnackCodeIDRejected:        mqttConnackNameIDRejected,
	mqttConnackCodeServerUnavailable: mqttConnackNameServerUnavailable,
	mqttConnackCodeBadCredentials:    mqttConnackNameBadCredentials,
	mqttConnackCodeNotAuthorized:     mqttConnackNameNotAuthorized,
}

// mqttConnackName names a CONNACK return code (MQTT 3.1.1).
func mqttConnackName(code byte) string {
	return codeName(code, mqttConnackNames, "code-%d")
}
