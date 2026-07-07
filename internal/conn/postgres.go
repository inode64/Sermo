package conn

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/lib/pq"
)

func init() {
	Register(postgresProtocol{}, "postgresql")
}

// postgresProtocol probes a PostgreSQL server.
type postgresProtocol struct{}

func (postgresProtocol) Name() string       { return "postgres" }
func (postgresProtocol) DefaultPort() int   { return 5432 }
func (postgresProtocol) RequiresUser() bool { return true }

// Probe connects (authenticating with the configured user/password), verifies
// the server responds with a ping, and reads its version. The caller's context
// bounds the whole probe.
func (postgresProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	db, err := openPostgresDB(cfg)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = db.Close() }()

	if err := db.PingContext(ctx); err != nil {
		return Result{}, err
	}
	var version string
	// Best effort: a successful ping already proves connect + auth.
	// SHOW server_version gives a clean number (vs the verbose version()).
	_ = db.QueryRowContext(ctx, "SHOW server_version").Scan(&version)
	return Result{Version: version}, nil
}

// PostgresDSN renders a lib/pq connection URL from cfg (escaping the password).
// Exported so the sql check can open a PostgreSQL connection reusing this logic.
func PostgresDSN(cfg Config) string { return buildPGDSN(cfg) }

// openPostgresDB opens a PostgreSQL pool via lib/pq, routing TCP dials through
// BindDialer when cfg.Interface is set so multihomed probes egress the right link.
func openPostgresDB(cfg Config) (*sql.DB, error) {
	connector, err := postgresConnector(cfg)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(connector), nil
}

// postgresConnector builds the lib/pq connector for cfg, routing TCP dials
// through BindDialer when cfg.Interface is set. Tests also use it to verify
// interface binding is wired without opening a connection.
func postgresConnector(cfg Config) (*pq.Connector, error) {
	connector, err := pq.NewConnector(buildPGDSN(cfg))
	if err != nil {
		return nil, fmt.Errorf("postgres connector: %w", err)
	}
	if cfg.Interface != "" {
		connector.Dialer(pqDialer(cfg.Interface))
	}
	return connector, nil
}

// buildPGDSN renders a lib/pq connection URL from cfg. A URL (with
// url.UserPassword) escapes special characters in the password correctly.
func buildPGDSN(cfg Config) string {
	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}
	port := cfg.Port
	if port == 0 {
		port = 5432
	}
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(cfg.User, cfg.Password),
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
		Path:   "/" + cfg.Database,
	}
	q := url.Values{}
	q.Set("sslmode", sslMode(cfg.TLS))
	for k, v := range cfg.Params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// sslMode maps the generic tls field to a PostgreSQL sslmode. Default disable
// (plaintext). "true"/"skip-verify" encrypt without strict verification
// (lib/pq "require"); the verify-* / prefer modes pass through.
func sslMode(tls string) string {
	switch strings.ToLower(strings.TrimSpace(tls)) {
	case "", "false", "no", "off", "disable":
		return "disable"
	case "true", "yes", "on", "require":
		return "require"
	case tlsSkipVerify:
		return "require"
	case "prefer":
		return "prefer"
	case "verify-ca":
		return "verify-ca"
	case "verify-full":
		return "verify-full"
	default:
		return tls // allow a valid sslmode passed through
	}
}
