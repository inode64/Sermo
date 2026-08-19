package conn

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

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
	db, err := OpenPostgresDB(ctx, cfg)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = db.Close() }()

	// SHOW server_version gives a clean number (vs the verbose version()).
	return pingAndVersion(ctx, db, "SHOW server_version")
}

// PostgresDSN renders a PostgreSQL connection URL from cfg (escaping the password).
// Exported so the sql check can open a PostgreSQL connection reusing this logic.
func PostgresDSN(cfg Config) string { return buildPGDSN(cfg) }

// OpenPostgresDB opens a PostgreSQL pool via pgx, routing TCP dials through
// BindDialer when cfg.Interface is set so multihomed probes egress the right link.
func OpenPostgresDB(ctx context.Context, cfg Config) (*sql.DB, error) {
	config, err := postgresConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return stdlib.OpenDB(*config), nil
}

// postgresConfig builds the pgx connection config for cfg, routing TCP dials
// through BindDialer. Tests also use it to verify
// interface binding is wired without opening a connection.
func postgresConfig(ctx context.Context, cfg Config) (*pgx.ConnConfig, error) {
	target := probeTargetFor(ctx, cfg, defaultPortPostgres)
	config, err := pgx.ParseConfig(buildPGDSNWithTarget(cfg, target))
	if err != nil {
		return nil, fmt.Errorf("postgres config: %w", err)
	}
	config.DialFunc = target.dialer().DialContext
	return config, nil
}

// buildPGDSN renders a PostgreSQL connection URL from cfg. A URL (with
// url.UserPassword) escapes special characters in the password correctly.
func buildPGDSN(cfg Config) string {
	return buildPGDSNWithTarget(cfg, newProbeTarget(cfg, defaultPortPostgres))
}

func buildPGDSNWithTarget(cfg Config, target probeTarget) string {
	u := url.URL{
		Scheme: ProtocolNamePostgres,
		User:   url.UserPassword(cfg.User, cfg.Password),
		Host:   target.address(),
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
// ("require"); the verify-* / prefer modes pass through.
func sslMode(tls string) string {
	switch strings.ToLower(strings.TrimSpace(tls)) {
	case "", tlsModeFalse, tlsModeNo, tlsModeOff, tlsDisable:
		return tlsDisable
	case ParamValueTrue, tlsModeYes, tlsModeOn, tlsRequire:
		return tlsRequire
	case tlsSkipVerify:
		return tlsRequire
	case tlsPrefer:
		return tlsPrefer
	case tlsVerifyCA:
		return tlsVerifyCA
	case tlsVerifyFull:
		return tlsVerifyFull
	default:
		return tls // allow a valid sslmode passed through
	}
}
