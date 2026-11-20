package conn

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"

	"sermo/internal/netutil"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// PostgresDriverName is pgx's database/sql driver name.
const PostgresDriverName = "pgx"

// postgresProtocol probes a PostgreSQL server.
type postgresProtocol struct{}

func (postgresProtocol) Name() string       { return ProtocolNamePostgres }
func (postgresProtocol) DefaultPort() int   { return defaultPortPostgres }
func (postgresProtocol) RequiresUser() bool { return true }

// Probe connects (authenticating with the configured user/password), verifies
// the server responds with a ping, and reads its version. The caller's context
// bounds the whole probe.
func (postgresProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	db, err := OpenPostgresDB(cfg)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = db.Close() }()

	// SHOW server_version gives a clean number (vs the verbose version()).
	return queryVersion(ctx, db, "SHOW server_version")
}

// OpenPostgresDB opens a PostgreSQL pool via pgx, routing TCP dials through
// BindDialer when cfg.Interface is set so multihomed probes egress the right link.
func OpenPostgresDB(cfg Config) (*sql.DB, error) {
	config, err := postgresConfig(cfg)
	if err != nil {
		return nil, err
	}
	return stdlib.OpenDB(*config), nil
}

// postgresConfig builds the pgx connection config for cfg, routing TCP dials
// through BindDialer, or dialing cfg.Socket when set. Tests also use it to
// verify interface binding is wired without opening a connection.
func postgresConfig(cfg Config) (*pgx.ConnConfig, error) {
	target := newProbeTarget(cfg, defaultPortPostgres)
	config, err := pgx.ParseConfig(buildPGDSNWithTarget(target))
	if err != nil {
		return nil, fmt.Errorf("postgres config: %w", err)
	}
	if cfg.Socket == "" {
		config.DialFunc = target.dialer().DialContext
		return config, nil
	}
	// The server offers no TLS on its Unix socket, so like libpq the socket
	// ignores tls. A directory host is what makes pgx skip the TLS handshake
	// and the TCP fallbacks; the dial still goes to the configured socket file.
	config.Host = filepath.Dir(cfg.Socket)
	config.TLSConfig, config.Fallbacks = nil, nil
	config.DialFunc = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialUnix(ctx, cfg.Socket)
	}
	return config, nil
}

func buildPGDSNWithTarget(target probeTarget) string {
	cfg := target.cfg
	u := url.URL{
		Scheme: ProtocolNamePostgres,
		User:   url.UserPassword(cfg.User, cfg.Password),
		Host:   target.address(),
		Path:   "/" + cfg.Database,
	}
	q := url.Values{}
	q.Set("sslmode", sslMode(cfg.TLS))
	u.RawQuery = q.Encode()
	return u.String()
}

// sslMode maps the generic tls field to a PostgreSQL sslmode. Default disable
// (plaintext). The shared spellings keep their meaning for every protocol:
// "true" (and yes/on/required) is verified TLS, so it maps to verify-full, and
// only "skip-verify" encrypts without verification ("require"). The native
// sslmodes pass through.
func sslMode(tls string) string {
	// The shared friendly spellings (true/false/yes/no/on/off/required/skip-verify)
	// are netutil's; only pgx's own sslmode names are translated here.
	switch mode := netutil.NormalizeTLS(tls); mode {
	case "":
		return tlsDisable
	case netutil.TLSModeTrue:
		return tlsVerifyFull
	case netutil.TLSModeSkipVerify:
		return tlsRequire
	default:
		return strings.ToLower(strings.TrimSpace(mode)) // a native sslmode passes through
	}
}
