package conn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
)

func init() { Register(tftpProtocol{}) }

// TFTP opcodes (RFC 1350).
const (
	tftpRRQ   = 1
	tftpDATA  = 3
	tftpERROR = 5
	tftpOACK  = 6
)

const (
	tftpDefaultProbeFilename = "sermo-tftp-check"
	tftpModeOctet            = "octet"
	tftpReplyBufferBytes     = 1024
	tftpReplyMinBytes        = 4
	tftpWireByteShift        = 8
	tftpWireZeroByte         = 0
)

const (
	tftpReplyOpcodeHighOffset = 0
	tftpReplyOpcodeLowOffset  = 1
	tftpErrorCodeHighOffset   = 2
	tftpErrorCodeLowOffset    = 3
	tftpErrorMessageOffset    = 4
)

const (
	tftpOpNameData  = "data"
	tftpOpNameError = "error"
	tftpOpNameOACK  = "oack"
)

// tftpProtocol probes a TFTP server natively (RFC 1350): it sends a read request
// (RRQ) and verifies the server answers with a valid TFTP packet. A DATA reply
// means the file is being served; an ERROR reply (e.g. "file not found") still
// proves the server is up and speaking TFTP. No authentication.
type tftpProtocol struct{}

func (tftpProtocol) Name() string       { return ProtocolNameTFTP }
func (tftpProtocol) DefaultPort() int   { return defaultPortTFTP }
func (tftpProtocol) RequiresUser() bool { return false }

func (tftpProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}
	port := cfg.Port
	if port == 0 {
		port = defaultPortTFTP
	}
	server, err := net.ResolveUDPAddr(networkUDP, hostPort(host, port))
	if err != nil {
		return Result{}, err
	}

	// An unconnected socket: a TFTP server replies from a fresh ephemeral port
	// (the transfer TID), not from port 69, so a connected socket would drop it.
	lc := BindListenConfig(cfg.Interface)
	pc, err := lc.ListenPacket(ctx, networkUDP, ":0")
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = pc.Close() }()
	applyDeadline(ctx, pc)

	filename := cfg.Query
	if filename == "" {
		filename = tftpDefaultProbeFilename
	}
	if _, err := pc.WriteTo(buildTFTPReadRequest(filename), server); err != nil {
		return Result{}, err
	}
	buf := make([]byte, tftpReplyBufferBytes)
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		return Result{}, err
	}
	opcode, errCode, msg, err := parseTFTPReply(buf[:n])
	if err != nil {
		return Result{}, err
	}
	if !tftpResponded(opcode) {
		return Result{}, fmt.Errorf("unexpected TFTP opcode %d", opcode)
	}

	extra := map[string]string{extraQuery: filename, extraReply: tftpOpName(opcode)}
	if opcode == tftpERROR {
		extra[extraTFTPErrorCode] = strconv.Itoa(errCode)
		extra[extraTFTPError] = msg
	}
	return Result{Extra: extra}, nil
}

// buildTFTPReadRequest builds an RRQ for filename in octet (binary) mode.
func buildTFTPReadRequest(filename string) []byte {
	var b bytes.Buffer
	b.WriteByte(tftpWireZeroByte)
	b.WriteByte(tftpRRQ)
	b.WriteString(filename)
	b.WriteByte(tftpWireZeroByte)
	b.WriteString(tftpModeOctet)
	b.WriteByte(tftpWireZeroByte)
	return b.Bytes()
}

// parseTFTPReply reads the opcode of a TFTP reply, plus the error code and
// message for an ERROR packet.
func parseTFTPReply(b []byte) (opcode, errCode int, msg string, err error) {
	if len(b) < tftpReplyMinBytes {
		return 0, 0, "", errors.New("short TFTP reply")
	}
	opcode = int(b[tftpReplyOpcodeHighOffset])<<tftpWireByteShift | int(b[tftpReplyOpcodeLowOffset])
	if opcode == tftpERROR {
		errCode = int(b[tftpErrorCodeHighOffset])<<tftpWireByteShift | int(b[tftpErrorCodeLowOffset])
		if i := bytes.IndexByte(b[tftpErrorMessageOffset:], tftpWireZeroByte); i >= 0 {
			msg = string(b[tftpErrorMessageOffset : tftpErrorMessageOffset+i])
		} else {
			msg = string(b[tftpErrorMessageOffset:])
		}
	}
	return opcode, errCode, msg, nil
}

// tftpResponded reports whether opcode is a valid server reply (DATA, ERROR or
// OACK) — any of which proves the server is up and speaking TFTP.
func tftpResponded(opcode int) bool {
	return opcode == tftpDATA || opcode == tftpERROR || opcode == tftpOACK
}

func tftpOpName(opcode int) string {
	switch opcode {
	case tftpDATA:
		return tftpOpNameData
	case tftpERROR:
		return tftpOpNameError
	case tftpOACK:
		return tftpOpNameOACK
	default:
		return strconv.Itoa(opcode)
	}
}
