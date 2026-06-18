package checks

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
)

// mongoCheck runs a MongoDB query and compares a scalar result against a value.
// It is condition-style like the sql check: OK == true means the comparison
// holds. Three query shapes are supported:
//   - count:     a `collection` (+ optional JSON `filter`) — compares the
//     matching document count.
//   - aggregate: a `collection` + JSON `pipeline` — runs the pipeline and pulls
//     the scalar at the dotted `result` path from the first document.
//   - command:   a JSON `command` run on the database — pulls the scalar at the
//     dotted `result` path from the reply.
//
// Connection variables (host/port/user/password/database/tls, plus auth_source)
// mirror the mysql/mariadb checks and reuse conn.MongoConnect. Use a read-only
// user; the query is run as given.
type mongoCheck struct {
	base
	cfg        conn.Config
	mode       string // count | aggregate | command
	database   string
	collection string
	filter     any
	pipeline   any
	command    any
	resultPath []string // dotted result path (aggregate/command)
	op         string
	value      string
}

func (c mongoCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	client, err := conn.MongoConnect(c.cfg)
	if err != nil {
		return c.result(false, "mongodb: "+err.Error(), start)
	}
	defer conn.MongoDisconnect(client)

	result, err := c.scalar(ctx, client)
	if err != nil {
		return c.result(false, "mongodb: "+err.Error(), start)
	}

	ok, err := compareValue(result, c.op, c.value)
	if err != nil {
		return c.result(false, "mongodb: "+err.Error(), start)
	}
	res := c.result(ok, fmt.Sprintf("mongodb: %q %s %q = %t", result, c.op, c.value, ok), start)
	data := map[string]any{"op": c.op, "threshold": c.value, "result": result, "mode": c.mode}
	if f, perr := strconv.ParseFloat(strings.TrimSpace(result), 64); perr == nil {
		data["value"] = f
	}
	res.Data = data
	return res
}

// scalar runs the configured query and returns the scalar to compare.
func (c mongoCheck) scalar(ctx context.Context, client *mongo.Client) (string, error) {
	db := client.Database(c.database)
	switch c.mode {
	case "count":
		n, err := db.Collection(c.collection).CountDocuments(ctx, c.filter)
		if err != nil {
			return "", err
		}
		return strconv.FormatInt(n, 10), nil
	case "aggregate":
		cur, err := db.Collection(c.collection).Aggregate(ctx, c.pipeline)
		if err != nil {
			return "", err
		}
		defer func() { _ = cur.Close(ctx) }()
		if !cur.Next(ctx) {
			if err := cur.Err(); err != nil {
				return "", err
			}
			return "", errors.New("aggregation returned no documents")
		}
		return mongoLookup(cur.Current, c.resultPath)
	default: // command
		raw, err := db.RunCommand(ctx, c.command).Raw()
		if err != nil {
			return "", err
		}
		return mongoLookup(raw, c.resultPath)
	}
}

// mongoLookup pulls the scalar at a dotted path from a BSON document.
func mongoLookup(raw bson.Raw, path []string) (string, error) {
	rv, err := raw.LookupErr(path...)
	if err != nil {
		return "", fmt.Errorf("result path %q not found", strings.Join(path, "."))
	}
	s, ok := mongoRawScalar(rv)
	if !ok {
		return "", fmt.Errorf("result path %q is not a scalar value", strings.Join(path, "."))
	}
	return s, nil
}

// mongoRawScalar renders a scalar BSON value (string/number/bool) as a string for
// comparison. Non-scalar values (documents, arrays, dates) report false.
func mongoRawScalar(rv bson.RawValue) (string, bool) {
	switch rv.Type {
	case bson.TypeString:
		return rv.StringValue(), true
	case bson.TypeInt32:
		return strconv.FormatInt(int64(rv.Int32()), 10), true
	case bson.TypeInt64:
		return strconv.FormatInt(rv.Int64(), 10), true
	case bson.TypeDouble:
		return strconv.FormatFloat(rv.Double(), 'f', -1, 64), true
	case bson.TypeBoolean:
		return strconv.FormatBool(rv.Boolean()), true
	default:
		return "", false
	}
}

// buildMongoCheck builds a mongodb-query check, resolving the connection (reusing
// the mysql-style host/port/user/password/database/tls fields) and the query
// shape (count / aggregate / command).
func buildMongoCheck(b base, entry map[string]any) (Check, string) {
	op := cfgval.AsString(entry["op"])
	if !validCompareOp(op) {
		return nil, "mongodb-query check op must be one of ==, !=, >, >=, <, <=, =~"
	}
	value := cfgval.String(entry["value"])
	if value == "" {
		return nil, "mongodb-query check requires a value"
	}

	collection := cfgval.AsString(entry["collection"])
	command := cfgval.AsString(entry["command"])
	pipeline := cfgval.AsString(entry["pipeline"])
	resultPath := cfgval.AsString(entry["result"])

	c := mongoCheck{base: b, cfg: mongoConnConfig(entry), database: cfgval.AsString(entry["database"]), collection: collection, op: op, value: value}

	switch {
	case command != "":
		if collection != "" || pipeline != "" {
			return nil, "mongodb-query check: command cannot be combined with collection/pipeline"
		}
		if resultPath == "" {
			return nil, "mongodb-query check: command requires a result path"
		}
		doc, err := parseMongoDoc(command)
		if err != nil {
			return nil, "mongodb-query check: invalid command JSON: " + err.Error()
		}
		if c.database == "" {
			c.database = "admin"
		}
		c.mode, c.command, c.resultPath = "command", doc, strings.Split(resultPath, ".")
	case collection != "":
		if c.database == "" {
			return nil, "mongodb-query check: a collection query requires a database"
		}
		if pipeline != "" {
			if resultPath == "" {
				return nil, "mongodb-query check: pipeline requires a result path"
			}
			pl, err := parseMongoPipeline(pipeline)
			if err != nil {
				return nil, "mongodb-query check: invalid pipeline JSON: " + err.Error()
			}
			c.mode, c.pipeline, c.resultPath = "aggregate", pl, strings.Split(resultPath, ".")
		} else {
			filter := bson.D{}
			if f := cfgval.AsString(entry["filter"]); f != "" {
				d, err := parseMongoDoc(f)
				if err != nil {
					return nil, "mongodb-query check: invalid filter JSON: " + err.Error()
				}
				filter = d
			}
			c.mode, c.filter = "count", filter
		}
	default:
		return nil, "mongodb-query check requires a collection (+filter), a collection+pipeline, or a command"
	}
	return c, ""
}

// mongoConnConfig builds a conn.Config for a mongodb-query check, defaulting the
// port to MongoDB's standard port (via the conn registry) and carrying an
// optional auth_source.
func mongoConnConfig(entry map[string]any) conn.Config {
	cfg := conn.Config{
		Host:     cfgval.AsString(entry["host"]),
		User:     cfgval.AsString(entry["user"]),
		Password: cfgval.AsString(entry["password"]),
		Database: cfgval.AsString(entry["database"]),
		TLS:      tlsString(entry["tls"]),
	}
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	cfg.Port = 27017
	if proto, ok := conn.Lookup("mongodb"); ok {
		cfg.Port = proto.DefaultPort()
	}
	if p, ok := cfgval.Int(entry["port"]); ok {
		cfg.Port = p
	}
	if as := cfgval.AsString(entry["auth_source"]); as != "" {
		cfg.Params = map[string]string{"auth_source": as}
	}
	return cfg
}

// parseMongoDoc parses an (extended) JSON object into a BSON document.
func parseMongoDoc(s string) (bson.D, error) {
	var d bson.D
	if err := bson.UnmarshalExtJSON([]byte(s), false, &d); err != nil {
		return nil, err
	}
	return d, nil
}

// parseMongoPipeline parses an (extended) JSON array into an aggregation pipeline.
// ExtJSON requires a document at the top level, so the array is wrapped and the
// value extracted.
func parseMongoPipeline(s string) (any, error) {
	if !strings.HasPrefix(strings.TrimSpace(s), "[") {
		return nil, errors.New("pipeline must be a JSON array")
	}
	var wrap bson.D
	if err := bson.UnmarshalExtJSON([]byte(`{"p":`+s+`}`), false, &wrap); err != nil {
		return nil, err
	}
	for _, e := range wrap {
		if e.Key == "p" {
			return e.Value, nil
		}
	}
	return nil, errors.New("empty pipeline")
}
