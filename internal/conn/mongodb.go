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

// mongoDisconnectTimeout bounds teardown after the operation context expires.
const mongoDisconnectTimeout = 5 * time.Second

// mongodbProtocol probes a MongoDB server.
type mongodbProtocol struct{}

func (mongodbProtocol) Name() string       { return "mongodb" }
func (mongodbProtocol) DefaultPort() int   { return 27017 }
func (mongodbProtocol) RequiresUser() bool { return false }

// Probe pings MongoDB and returns version/topology extras.
func (mongodbProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	client, err := MongoConnect(cfg)
	if err != nil {
		return Result{}, err
	}
	defer MongoDisconnect(client)

	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		return Result{}, err
	}
	var info struct {
		Version string `bson:"version"`
	}
	// Best effort: a successful ping already proves connect + auth.
	_ = client.Database("admin").RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&info)

	// Topology is best effort; ping already proved liveness.
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

// MongoConnect builds a lazy MongoDB client from cfg.
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

// MongoDisconnect closes a MongoDB client with the bounded teardown timeout.
func MongoDisconnect(client *mongo.Client) {
	if client == nil {
		return
	}
	dctx, cancel := context.WithTimeout(context.Background(), mongoDisconnectTimeout)
	defer cancel()
	_ = client.Disconnect(dctx)
}
