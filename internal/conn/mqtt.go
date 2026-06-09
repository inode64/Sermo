package conn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
)

func init() { Register(mqttProtocol{}) }

// mqttProtocol probes an MQTT broker natively (MQTT 3.1.1): it performs the
// CONNECT handshake and verifies the broker answers with a CONNACK accepting the
// connection (return code 0). With no credentials it is an anonymous connect;
// `user`/`password` authenticate. `tls` enables MQTTS (port 8883). A CONNACK is
// proof the broker speaks MQTT; a non-zero return code (refused) fails the check
// with the reason.
type mqttProtocol struct{}

func (mqttProtocol) Name() string       { return "mqtt" }
func (mqttProtocol) DefaultPort() int   { return 1883 }
func (mqttProtocol) RequiresUser() bool { return false }

func (mqttProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	port := cfg.Port
	if port == 0 {
		port = 1883
	}
	c, err := dialConn(ctx, cfg.Host, port, cfg.TLS)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}

	if _, err := c.Write(buildMQTTConnect("sermo-check", cfg.User, cfg.Password)); err != nil {
		return Result{}, err
	}
	var ack [4]byte
	if _, err := io.ReadFull(c, ack[:]); err != nil {
		return Result{}, err
	}
	code, sessionPresent, err := parseMQTTConnack(ack[:])
	if err != nil {
		return Result{}, err
	}
	if code != 0 {
		return Result{}, fmt.Errorf("MQTT connection refused: %s", mqttConnackName(code))
	}
	extra := map[string]string{"connack": mqttConnackName(code)}
	if sessionPresent {
		extra["session_present"] = "true"
	}
	return Result{Extra: extra}, nil
}

// buildMQTTConnect builds an MQTT 3.1.1 CONNECT packet with a clean session and
// optional username/password.
func buildMQTTConnect(clientID, user, pass string) []byte {
	var vh bytes.Buffer
	writeMQTTString(&vh, "MQTT")
	vh.WriteByte(0x04)  // protocol level (MQTT 3.1.1)
	flags := byte(0x02) // clean session
	if user != "" {
		flags |= 0x80
	}
	if pass != "" {
		flags |= 0x40
	}
	vh.WriteByte(flags)
	vh.WriteByte(0x00) // keep-alive high byte
	vh.WriteByte(0x3C) // keep-alive low byte (60s)
	writeMQTTString(&vh, clientID)
	if user != "" {
		writeMQTTString(&vh, user)
	}
	if pass != "" {
		writeMQTTString(&vh, pass)
	}

	var pkt bytes.Buffer
	pkt.WriteByte(0x10) // CONNECT control packet
	writeMQTTRemainingLength(&pkt, vh.Len())
	pkt.Write(vh.Bytes())
	return pkt.Bytes()
}

// writeMQTTString writes a 2-byte big-endian length-prefixed UTF-8 string.
func writeMQTTString(b *bytes.Buffer, s string) {
	n := len(s)
	b.WriteByte(byte(n >> 8))
	b.WriteByte(byte(n))
	b.WriteString(s)
}

// writeMQTTRemainingLength writes the MQTT variable-length "remaining length".
func writeMQTTRemainingLength(b *bytes.Buffer, n int) {
	for {
		d := byte(n % 128)
		n /= 128
		if n > 0 {
			d |= 0x80
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
	if len(b) < 4 {
		return 0, false, errors.New("short MQTT CONNACK")
	}
	if b[0] != 0x20 {
		return 0, false, fmt.Errorf("not an MQTT CONNACK (0x%02x)", b[0])
	}
	return b[3], b[2]&0x01 != 0, nil
}

// mqttConnackName names a CONNACK return code (MQTT 3.1.1).
func mqttConnackName(code byte) string {
	switch code {
	case 0:
		return "accepted"
	case 1:
		return "unacceptable-protocol-version"
	case 2:
		return "identifier-rejected"
	case 3:
		return "server-unavailable"
	case 4:
		return "bad-username-or-password"
	case 5:
		return "not-authorized"
	default:
		return fmt.Sprintf("code-%d", code)
	}
}
