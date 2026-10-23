package conn

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"

	"github.com/go-sql-driver/mysql"

	"sermo/internal/netutil"
)

// mysqlProtocol probes a MySQL or MariaDB server. With no credentials it reads
// the server's unauthenticated handshake greeting for a liveness + version
// check; with a user/password it performs a full authenticated ping.
type mysqlProtocol struct{}

func (mysqlProtocol) Name() string     { return ProtocolNameMySQL }
func (mysqlProtocol) DefaultPort() int { return defaultPortMySQL }

// RequiresUser is false: a credential-free probe reads the server's initial
// handshake (proving liveness, like smtp/amqp). A user only enables the deeper
// authenticated ping.
func (mysqlProtocol) RequiresUser() bool { return false }

// Probe checks a MySQL/MariaDB server. Without a user or password it reads the
// server's initial handshake packet — sent unprompted on connect — proving the
// peer is a live MySQL/MariaDB server and reporting its version, with no
// credentials (the smtp/amqp greeting model). With credentials it authenticates
// and pings via the driver. The caller's context bounds the whole probe.
func (mysqlProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	if cfg.User == "" && cfg.Password == "" {
		// The MySQL handshake is always sent in plaintext (TLS is negotiated
		// afterwards), so dial without TLS regardless of cfg.TLS.
		plain := cfg
		plain.TLS = ""
		c, err := newProbeTarget(plain, defaultPortMySQL).openStream(ctx)
		if err != nil {
			return Result{}, err
		}
		defer func() { _ = c.Close() }()
		return mysqlGreeting(c)
	}

	db, err := OpenMySQLDB(cfg)
	if err != nil {
		return Result{}, probeErr(ProtocolNameMySQL, stepOpen, err)
	}
	defer func() { _ = db.Close() }()

	return queryVersion(ctx, db, "SELECT VERSION()")
}

// OpenMySQLDB opens a MySQL pool whose connector retains the bound DialFunc;
// formatting the config back to a DSN would discard that function.
func OpenMySQLDB(cfg Config) (*sql.DB, error) {
	target := newProbeTarget(cfg, defaultPortMySQL)
	connector, err := mysql.NewConnector(buildMySQLConfigWithTarget(target))
	if err != nil {
		return nil, fmt.Errorf("mysql connector: %w", err)
	}
	return sql.OpenDB(connector), nil
}

// maxMySQLHandshake bounds the initial handshake packet we are willing to read,
// so a non-MySQL or hostile peer cannot make the probe allocate without limit.
// The real packet is well under a kilobyte.
const maxMySQLHandshake = 1 << 16

const (
	mysqlPacketHeaderBytes            = 4
	mysqlMinPayloadBytes              = 1
	mysqlPayloadLengthLowOffset       = 0
	mysqlPayloadLengthMidOffset       = 1
	mysqlPayloadLengthHighOffset      = 2
	mysqlPayloadLengthMidShift        = 8
	mysqlPayloadLengthHighShift       = 16
	mysqlProtocolVersionOffset        = 0
	mysqlServerVersionOffset          = 1
	mysqlProtocolVersion10       byte = 0x0a
	mysqlPacketERR               byte = 0xff
	mysqlERRCodeOffset                = 1
	mysqlERRCodeHighShift             = 8
	mysqlERRMessageOffset             = 3
	// mysqlERRSQLStateMarker opens the optional "#" + 5-character SQLSTATE
	// that precedes the message in protocol-4.1 ERR packets.
	mysqlERRSQLStateMarker byte = '#'
	mysqlERRSQLStateBytes       = 6
	mysqlNullTerminator    byte = 0
)

// mysqlGreeting reads the server's Initial Handshake Packet — sent unprompted on
// connect, before authentication — and extracts the server version. A
// well-formed protocol-10 handshake proves the peer is a live MySQL/MariaDB
// server without any credentials. An ERR packet (0xff) means the server answered
// but refused the connection: a saturation refusal (too many connections, host
// blocked, …) is a busy verdict, any other code an error carrying the server's
// message and number.
func mysqlGreeting(r io.Reader) (Result, error) {
	var hdr [mysqlPacketHeaderBytes]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Result{}, probeErr(ProtocolNameMySQL, stepMySQLHandshakeHeader, err)
	}
	// Packet header: 3-byte little-endian payload length, then a sequence id.
	n := int(hdr[mysqlPayloadLengthLowOffset]) |
		int(hdr[mysqlPayloadLengthMidOffset])<<mysqlPayloadLengthMidShift |
		int(hdr[mysqlPayloadLengthHighOffset])<<mysqlPayloadLengthHighShift
	if n < mysqlMinPayloadBytes || n > maxMySQLHandshake {
		return Result{}, fmt.Errorf("mysql: implausible handshake length %d", n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return Result{}, probeErr(ProtocolNameMySQL, stepMySQLHandshakePayload, err)
	}

	switch payload[mysqlProtocolVersionOffset] {
	case mysqlProtocolVersion10:
		// server_version: a null-terminated string right after the version byte.
		ver := payload[mysqlServerVersionOffset:]
		if i := bytes.IndexByte(ver, mysqlNullTerminator); i >= 0 {
			ver = ver[:i]
		}
		return Result{Version: string(ver)}, nil
	case mysqlPacketERR:
		number, msg := mysqlERRPacket(payload)
		if busy, ok := mysqlBusy(number, msg); ok {
			return busyResult(Result{}, busy), nil
		}
		return Result{}, fmt.Errorf("mysql: server refused connection: %s (%d)", msg, number)
	default:
		return Result{}, fmt.Errorf("mysql: unexpected handshake protocol byte 0x%02x (not MySQL)", payload[mysqlProtocolVersionOffset])
	}
}

// mysqlERRPacket decodes an ERR packet payload (0xff, a 2-byte little-endian
// error number, an optional "#SQLSTATE", then the message).
func mysqlERRPacket(payload []byte) (number uint16, message string) {
	if len(payload) > mysqlERRCodeOffset+1 {
		number = uint16(payload[mysqlERRCodeOffset]) | uint16(payload[mysqlERRCodeOffset+1])<<mysqlERRCodeHighShift
	}
	if len(payload) <= mysqlERRMessageOffset {
		return number, ""
	}
	rest := payload[mysqlERRMessageOffset:]
	if rest[0] == mysqlERRSQLStateMarker && len(rest) >= mysqlERRSQLStateBytes {
		rest = rest[mysqlERRSQLStateBytes:]
	}
	return number, strings.TrimSpace(string(rest))
}

// buildMySQLConfigWithTarget binds TCP dials to cfg.Interface when configured.
func buildMySQLConfigWithTarget(target probeTarget) *mysql.Config {
	cfg := target.cfg
	c := mysql.NewConfig()
	c.Net = networkTCP
	c.Addr = target.address()
	c.User = cfg.User
	c.Passwd = cfg.Password
	c.DBName = cfg.Database
	switch {
	case cfg.Socket != "":
		// The authenticated path must reach the same endpoint as the greeting
		// probe; over TCP the server would also apply other user@host grants.
		c.Net = networkUnix
		c.Addr = cfg.Socket
	case cfg.Interface != "":
		c.DialFunc = target.dialer().DialContext
	}
	if tls := netutil.NormalizeTLS(cfg.TLS); tls != "" {
		c.TLSConfig = tls
	}
	return c
}
