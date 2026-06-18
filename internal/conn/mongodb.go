package conn

import (
	"context"
	"net"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

func init() { Register(mongodbProtocol{}, "mongo") }

// mongoDisconnectTimeout bounds the client teardown so an unreachable server
// cannot hang the probe in its deferred Disconnect (the probe's own context may
// already be expired by then, so a fresh bounded context is used).
const mongoDisconnectTimeout = 5 * time.Second

// mongodbProtocol probes a MongoDB server.
type mongodbProtocol struct{}

func (mongodbProtocol) Name() string       { return "mongodb" }
func (mongodbProtocol) DefaultPort() int   { return 27017 }
func (mongodbProtocol) RequiresUser() bool { return false }

// Probe connects (authenticating with the configured user/password when set),
// verifies the server responds to a ping, and reads its version via buildInfo.
// The caller's context bounds the whole probe.
func (mongodbProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	client, err := MongoConnect(cfg)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		dctx, cancel := context.WithTimeout(context.Background(), mongoDisconnectTimeout)
		defer cancel()
		_ = client.Disconnect(dctx)
	}()

	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		return Result{}, err
	}
	var info struct {
		Version string `bson:"version"`
	}
	// Best effort: a successful ping already proves connect + auth.
	_ = client.Database("admin").RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&info)

	// Topology: hello/isMaster expose the replica-set role so an expect: rule can
	// assert it (e.g. role == primary, set_name == rs0). Best effort; it runs
	// pre-auth and never fails the probe.
	role, setName, readOnly := mongoTopology(ctx, client)
	extra := map[string]string{"role": role, "read_only": strconv.FormatBool(readOnly)}
	putIfSet(extra, "set_name", setName)
	return Result{Version: info.Version, Extra: extra}, nil
}

// mongoTopology runs hello (MongoDB 5.0+) — falling back to the legacy isMaster
// on older servers — and derives the node's replica-set role plus its set name
// and read-only flag.
func mongoTopology(ctx context.Context, client *mongo.Client) (role, setName string, readOnly bool) {
	var h struct {
		IsWritablePrimary bool   `bson:"isWritablePrimary"`
		IsMaster          bool   `bson:"ismaster"`
		Secondary         bool   `bson:"secondary"`
		ArbiterOnly       bool   `bson:"arbiterOnly"`
		SetName           string `bson:"setName"`
		ReadOnly          bool   `bson:"readOnly"`
	}
	if client.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&h) != nil {
		_ = client.Database("admin").RunCommand(ctx, bson.D{{Key: "isMaster", Value: 1}}).Decode(&h)
	}
	return mongoRole(h.IsWritablePrimary || h.IsMaster, h.Secondary, h.ArbiterOnly, h.SetName), h.SetName, h.ReadOnly
}

// mongoRole classifies a node from its hello/isMaster flags: a member with no
// set name is a standalone; otherwise arbiter/primary/secondary, defaulting to
// "unknown" for a replica-set member in a transient state.
func mongoRole(primary, secondary, arbiter bool, setName string) string {
	switch {
	case setName == "":
		return "standalone"
	case arbiter:
		return "arbiter"
	case primary:
		return "primary"
	case secondary:
		return "secondary"
	default:
		return "unknown"
	}
}

// MongoConnect builds a MongoDB client from cfg (host/port/user/password/tls and
// an optional `auth_source` param). The connection is lazy — the first operation,
// bounded by its context, surfaces connection errors. Exported so the
// mongodb-query check reuses the same connection logic (host/port/user/password/
// database/tls), mirroring MySQLDSN/PostgresDSN.
func MongoConnect(cfg Config) (*mongo.Client, error) {
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 27017
	}
	opts := options.Client().SetHosts([]string{net.JoinHostPort(host, strconv.Itoa(port))})
	if cfg.User != "" {
		// Auth database: an explicit auth_source, else the target database, else
		// admin (MongoDB's conventional credentials database).
		authSource := cfg.Params["auth_source"]
		if authSource == "" {
			authSource = cfg.Database
		}
		if authSource == "" {
			authSource = "admin"
		}
		opts.SetAuth(options.Credential{Username: cfg.User, Password: cfg.Password, AuthSource: authSource})
	}
	if mode := normalizeTLS(cfg.TLS); mode != "" {
		tc := tlsClientConfig(host)
		if mode == "skip-verify" {
			tc.InsecureSkipVerify = true //nolint:gosec // operator chose tls: skip-verify
		}
		opts.SetTLSConfig(tc)
	}
	return mongo.Connect(opts)
}
