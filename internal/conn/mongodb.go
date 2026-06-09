package conn

import (
	"context"
	"crypto/tls"
	"net"
	"strconv"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

func init() { Register(mongodbProtocol{}, "mongo") }

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
	defer func() { _ = client.Disconnect(context.Background()) }()

	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		return Result{}, err
	}
	var info struct {
		Version string `bson:"version"`
	}
	// Best effort: a successful ping already proves connect + auth.
	_ = client.Database("admin").RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&info)
	return Result{Version: info.Version}, nil
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
		tc := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
		if mode == "skip-verify" {
			tc.InsecureSkipVerify = true //nolint:gosec // operator chose tls: skip-verify
		}
		opts.SetTLSConfig(tc)
	}
	return mongo.Connect(opts)
}
