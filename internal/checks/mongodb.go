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

// mongoCheck compares a scalar MongoDB query result. OK means op/value matched.
// Supported shapes:
//   - count:     a `collection` (+ optional JSON `filter`) — compares the
//     matching document count.
//   - aggregate: a `collection` + JSON `pipeline` — runs the pipeline and pulls
//     the scalar at the dotted `result` path from the first document.
//   - command:   a JSON `command` run on the database — pulls the scalar at the
//     dotted `result` path from the reply.
//
// Use a read-only user; the query is run as given.
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

const (
	mongoModeCount     = "count"
	mongoModeAggregate = "aggregate"
	mongoModeCommand   = "command"
)

func (c mongoCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	client, err := conn.MongoConnect(c.cfg)
	if err != nil {
		return c.result(false, "mongodb: "+err.Error(), start)
	}
	defer func() { conn.MongoDisconnect(ctx, client) }()

	result, err := c.scalar(ctx, client)
	if err != nil {
		return c.result(false, "mongodb: "+err.Error(), start)
	}

	return finishScalarCompare(c.base, "mongodb", result, c.op, c.value, start, map[string]any{
		DataKeyMode: c.mode,
	})
}

// scalar runs the configured query and returns the scalar to compare.
func (c mongoCheck) scalar(ctx context.Context, client *mongo.Client) (string, error) {
	db := client.Database(c.database)
	switch c.mode {
	case mongoModeCount:
		n, err := db.Collection(c.collection).CountDocuments(ctx, c.filter)
		if err != nil {
			return "", err
		}
		return strconv.FormatInt(n, numericBaseDecimal), nil
	case mongoModeAggregate:
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
	default: // mongoModeCommand
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
		return strconv.FormatInt(int64(rv.Int32()), numericBaseDecimal), true
	case bson.TypeInt64:
		return strconv.FormatInt(rv.Int64(), numericBaseDecimal), true
	case bson.TypeDouble:
		return strconv.FormatFloat(rv.Double(), floatFormatFixed, floatPrecisionAuto, numericBits64), true
	case bson.TypeBoolean:
		return strconv.FormatBool(rv.Boolean()), true
	default:
		return "", false
	}
}

// buildMongoCheck builds a mongodb-query check.
func buildMongoCheck(b base, entry map[string]any) (Check, string) {
	op, value, msg := assertOpValue(entry, "mongodb-query")
	if msg != "" {
		return nil, msg
	}

	collection := cfgval.AsString(entry[CheckKeyCollection])
	command := cfgval.AsString(entry[CheckKeyCommand])
	pipeline := cfgval.AsString(entry[CheckKeyPipeline])
	resultPath := cfgval.AsString(entry[CheckKeyResult])

	c := mongoCheck{base: b, cfg: mongoConnConfig(entry), database: cfgval.AsString(entry[CheckKeyDatabase]), collection: collection, op: op, value: value}

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
		c.mode, c.command, c.resultPath = mongoModeCommand, doc, strings.Split(resultPath, ".")
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
			c.mode, c.pipeline, c.resultPath = mongoModeAggregate, pl, strings.Split(resultPath, ".")
		} else {
			filter := bson.D{}
			if f := cfgval.AsString(entry[CheckKeyFilter]); f != "" {
				d, err := parseMongoDoc(f)
				if err != nil {
					return nil, "mongodb-query check: invalid filter JSON: " + err.Error()
				}
				filter = d
			}
			c.mode, c.filter = mongoModeCount, filter
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
	cfg := databaseConnectionConfig(entry)
	cfg.Port = connectionPort(entry, conn.DefaultPort(conn.ProtocolNameMongoDB))
	if as := cfgval.AsString(entry[CheckKeyAuthSource]); as != "" {
		cfg.Params = map[string]string{conn.ParamKeyAuthSource: as}
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
