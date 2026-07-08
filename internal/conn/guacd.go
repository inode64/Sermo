package conn

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
)

func init() { Register(guacdProtocol{}, protocolAliasGuacamole) }

const (
	guacdDefaultProtocol = "vnc"
	guacdSelectOp        = "select"
	guacdInstructionEnd  = ';'
	guacdElementSep      = ','
	guacdLengthSep       = '.'
	guacdLengthSepWidth  = 1
	extraOpcode          = "opcode"
)

// guacdProtocol probes the Apache Guacamole proxy daemon (guacd) natively over
// the Guacamole protocol. It opens the handshake by sending a `select`
// instruction for a protocol (default "vnc", override with `query`) and verifies
// guacd replies with a well-formed Guacamole instruction — an `args` reply (the
// protocol is available) or an `error` (e.g. plugin missing) both prove guacd is
// up and speaking the protocol. No auth.
type guacdProtocol struct{}

func (guacdProtocol) Name() string       { return ProtocolNameGuacd }
func (guacdProtocol) DefaultPort() int   { return defaultPortGuacd }
func (guacdProtocol) RequiresUser() bool { return false }

func (guacdProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}
	port := cfg.Port
	if port == 0 {
		port = defaultPortGuacd
	}
	selectProto := cfg.Query
	if selectProto == "" {
		selectProto = guacdDefaultProtocol
	}

	c, err := BindDialer(cfg.Interface).DialContext(ctx, networkTCP, net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	applyDeadline(ctx, c)

	if _, err := io.WriteString(c, guacInstruction(guacdSelectOp, selectProto)); err != nil {
		return Result{}, err
	}
	line, err := bufio.NewReader(c).ReadString(guacdInstructionEnd)
	if err != nil && line == "" {
		return Result{}, err
	}
	opcode, err := parseGuacInstruction(line)
	if err != nil {
		return Result{}, err
	}
	return Result{Extra: map[string]string{extraSelect: selectProto, extraOpcode: opcode}}, nil
}

// guacInstruction encodes a Guacamole instruction: each element as
// "<length>.<value>", comma-separated, terminated by ';' (e.g.
// "6.select,3.vnc;"). Lengths are character counts (ASCII here).
func guacInstruction(elements ...string) string {
	var b strings.Builder
	for i, e := range elements {
		if i > 0 {
			b.WriteByte(guacdElementSep)
		}
		b.WriteString(strconv.Itoa(len(e)))
		b.WriteByte(guacdLengthSep)
		b.WriteString(e)
	}
	b.WriteByte(guacdInstructionEnd)
	return b.String()
}

// parseGuacInstruction reads the opcode (first element's value) of a Guacamole
// instruction, validating the "<length>.<value>" framing.
func parseGuacInstruction(s string) (string, error) {
	dot := strings.IndexByte(s, guacdLengthSep)
	if dot <= 0 {
		return "", errors.New("not a Guacamole instruction")
	}
	n, err := strconv.Atoi(s[:dot])
	if err != nil || n < 0 {
		return "", errors.New("invalid Guacamole element length")
	}
	start := dot + guacdLengthSepWidth
	if start+n > len(s) {
		return "", errors.New("truncated Guacamole instruction")
	}
	return s[start : start+n], nil
}
