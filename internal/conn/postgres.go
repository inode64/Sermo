package conn

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	"github.com/lib/pq"
)

// postgresProtocol probes a PostgreSQL server.
type postgresProtocol struct{}

func (postgresProtocol) Name() string       { return ProtocolNamePostgres }
func (postgresProtocol) DefaultPort() int   { return defaultPortPostgres }
func (postgresProtocol) RequiresUser() bool { return true }

// Probe connects (authenticating with the configured user/password), verifies
// the server responds with a ping, and reads its version. The caller's context
// bounds the whole probe.
func (postgresProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	db, err := openPostgresDB(ctx, cfg)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = db.Close() }()

	// SHOW server_version gives a clean number (vs the verbose version()).
	return pingAndVersion(ctx, db, "SHOW server_version")
}

// PostgresDSN renders a lib/pq connection URL from cfg (escaping the password).
// Exported so the sql check can open a PostgreSQL connection reusing this logic.
func PostgresDSN(cfg Config) string { return buildPGDSN(cfg) }

// openPostgresDB opens a PostgreSQL pool via lib/pq, routing TCP dials through
// BindDialer when cfg.Interface is set so multihomed probes egress the right link.
func openPostgresDB(ctx context.Context, cfg Config) (*sql.DB, error) {
	connector, err := postgresConnector(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(connector), nil
}

// postgresConnector builds the lib/pq connector for cfg, routing TCP dials
// through BindDialer when cfg.Interface is set. Tests also use it to verify
// interface binding is wired without opening a connection.
func postgresConnector(ctx context.Context, cfg Config) (*pq.Connector, error) {
	target := probeTargetFor(ctx, cfg, defaultPortPostgres)
	connector, err := pq.NewConnector(buildPGDSNWithTarget(cfg, target))
	if err != nil {
		return nil, fmt.Errorf("postgres connector: %w", err)
	}
	if cfg.Interface != "" {
		connector.Dialer(pqDialer(target))
	}
	return connector, nil
}

// buildPGDSN renders a lib/pq connection URL from cfg. A URL (with
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
// (lib/pq "require"); the verify-* / prefer modes pass through.
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
