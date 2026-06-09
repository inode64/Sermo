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

// tftpProtocol probes a TFTP server natively (RFC 1350): it sends a read request
// (RRQ) and verifies the server answers with a valid TFTP packet. A DATA reply
// means the file is being served; an ERROR reply (e.g. "file not found") still
// proves the server is up and speaking TFTP. No authentication.
type tftpProtocol struct{}

func (tftpProtocol) Name() string       { return "tftp" }
func (tftpProtocol) DefaultPort() int   { return 69 }
func (tftpProtocol) RequiresUser() bool { return false }

func (tftpProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 69
	}
	server, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return Result{}, err
	}

	// An unconnected socket: a TFTP server replies from a fresh ephemeral port
	// (the transfer TID), not from port 69, so a connected socket would drop it.
	pc, err := net.ListenPacket("udp", ":0") //nolint:gosec // G102: ephemeral client socket for the TFTP reply, not a server listener
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = pc.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = pc.SetDeadline(dl)
	}

	filename := cfg.Query
	if filename == "" {
		filename = "sermo-tftp-check"
	}
	if _, err := pc.WriteTo(buildTFTPReadRequest(filename), server); err != nil {
		return Result{}, err
	}
	buf := make([]byte, 1024)
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

	extra := map[string]string{"query": filename, "reply": tftpOpName(opcode)}
	if opcode == tftpERROR {
		extra["tftp_error_code"] = strconv.Itoa(errCode)
		extra["tftp_error"] = msg
	}
	return Result{Extra: extra}, nil
}

// buildTFTPReadRequest builds an RRQ for filename in octet (binary) mode.
func buildTFTPReadRequest(filename string) []byte {
	var b bytes.Buffer
	b.WriteByte(0)
	b.WriteByte(tftpRRQ)
	b.WriteString(filename)
	b.WriteByte(0)
	b.WriteString("octet")
	b.WriteByte(0)
	return b.Bytes()
}

// parseTFTPReply reads the opcode of a TFTP reply, plus the error code and
// message for an ERROR packet.
func parseTFTPReply(b []byte) (opcode, errCode int, msg string, err error) {
	if len(b) < 4 {
		return 0, 0, "", errors.New("short TFTP reply")
	}
	opcode = int(b[0])<<8 | int(b[1])
	if opcode == tftpERROR {
		errCode = int(b[2])<<8 | int(b[3])
		if i := bytes.IndexByte(b[4:], 0); i >= 0 {
			msg = string(b[4 : 4+i])
		} else {
			msg = string(b[4:])
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
		return "data"
	case tftpERROR:
		return "error"
	case tftpOACK:
		return "oack"
	default:
		return strconv.Itoa(opcode)
	}
}
