package conn

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"maps"
	"strings"

	"github.com/go-sql-driver/mysql"
)

func init() {
	// MariaDB speaks the MySQL wire protocol, so the same driver serves both.
	Register(mysqlProtocol{}, protocolAliasMariaDB)
}

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
		c, err := dialDeadline(ctx, plain, defaultPortMySQL)
		if err != nil {
			return Result{}, err
		}
		defer func() { _ = c.Close() }()
		return mysqlGreeting(c)
	}

	db, err := sql.Open(ProtocolNameMySQL, buildDSN(cfg))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = db.Close() }()

	return pingAndVersion(ctx, db, "SELECT VERSION()")
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
	mysqlERRMessageOffset             = 3
	mysqlNullTerminator          byte = 0
)

// mysqlGreeting reads the server's Initial Handshake Packet — sent unprompted on
// connect, before authentication — and extracts the server version. A
// well-formed protocol-10 handshake proves the peer is a live MySQL/MariaDB
// server without any credentials. An ERR packet (0xff) means the server answered
// but refused the connection (host blocked, too many connections, …); it is
// returned as an error carrying the server's message.
func mysqlGreeting(r io.Reader) (Result, error) {
	var hdr [mysqlPacketHeaderBytes]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Result{}, err
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
		return Result{}, err
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
		msg := ""
		if len(payload) > mysqlERRMessageOffset {
			msg = strings.TrimSpace(string(payload[mysqlERRMessageOffset:]))
		}
		return Result{}, fmt.Errorf("mysql: server refused connection: %s", msg)
	default:
		return Result{}, fmt.Errorf("mysql: unexpected handshake protocol byte 0x%02x (not MySQL)", payload[mysqlProtocolVersionOffset])
	}
}

// MySQLDSN renders a go-sql-driver DSN from cfg (escaping the password). Exported
// so the sql check can open a MySQL/MariaDB connection reusing this logic.
func MySQLDSN(cfg Config) string { return buildDSN(cfg) }

// buildMySQLConfig renders a go-sql-driver config from cfg. When cfg.Interface
// is set, TCP dials egress through BindDialer (SO_BINDTODEVICE).
func buildMySQLConfig(cfg Config) *mysql.Config {
	c := mysql.NewConfig()
	c.Net = networkTCP
	c.Addr = cfg.addrDefaults(defaultPortMySQL)
	c.User = cfg.User
	c.Passwd = cfg.Password
	c.DBName = cfg.Database
	if cfg.Interface != "" {
		d := BindDialer(cfg.Interface)
		c.DialFunc = d.DialContext
	}
	if tls := NormalizeTLS(cfg.TLS); tls != "" {
		c.TLSConfig = tls
	}
	if len(cfg.Params) > 0 {
		c.Params = map[string]string{}
		maps.Copy(c.Params, cfg.Params)
	}
	return c
}

// buildDSN renders a go-sql-driver DSN from cfg, using mysql.Config so that
// special characters in the password are escaped correctly.
func buildDSN(cfg Config) string {
	return buildMySQLConfig(cfg).FormatDSN()
}
