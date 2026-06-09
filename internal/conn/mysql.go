package conn

import (
	"context"
	"database/sql"
	"net"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"
)

func init() {
	// MariaDB speaks the MySQL wire protocol, so the same driver serves both.
	Register(mysqlProtocol{}, "mariadb")
}

// mysqlProtocol probes a MySQL or MariaDB server.
type mysqlProtocol struct{}

func (mysqlProtocol) Name() string       { return "mysql" }
func (mysqlProtocol) DefaultPort() int   { return 3306 }
func (mysqlProtocol) RequiresUser() bool { return true }

// Probe connects (authenticating with the configured user/password), verifies
// the server responds with a ping, and reads its version. The caller's context
// bounds the whole probe.
func (mysqlProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	db, err := sql.Open("mysql", buildDSN(cfg))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = db.Close() }()

	if err := db.PingContext(ctx); err != nil {
		return Result{}, err
	}
	var version string
	// Best effort: a successful ping already proves connect + auth.
	_ = db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version)
	return Result{Version: version}, nil
}

// buildDSN renders a go-sql-driver DSN from cfg, using mysql.Config so that
// special characters in the password are escaped correctly.
func buildDSN(cfg Config) string {
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 3306
	}
	c := mysql.NewConfig()
	c.Net = "tcp"
	c.Addr = net.JoinHostPort(host, strconv.Itoa(port))
	c.User = cfg.User
	c.Passwd = cfg.Password
	c.DBName = cfg.Database
	if tls := normalizeTLS(cfg.TLS); tls != "" {
		c.TLSConfig = tls
	}
	if len(cfg.Params) > 0 {
		c.Params = map[string]string{}
		for k, v := range cfg.Params {
			c.Params[k] = v
		}
	}
	return c.FormatDSN()
}

// normalizeTLS maps a friendly tls value to the go-sql-driver tls key. An empty
// result means plaintext (the driver default).
func normalizeTLS(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "false", "no", "off":
		return ""
	case "true", "yes", "on", "required":
		return "true"
	case "skip-verify", "skip_verify", "insecure":
		return "skip-verify"
	default:
		return s // allow a custom registered tls config name
	}
}
