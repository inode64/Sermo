package checks

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestMongoLookupAndScalar(t *testing.T) {
	raw, err := bson.Marshal(bson.M{
		"s":   "hi",
		"i":   int32(7),
		"l":   int64(9),
		"d":   1.5,
		"b":   true,
		"obj": bson.M{"n": int64(42)},
	})
	if err != nil {
		t.Fatal(err)
	}
	doc := bson.Raw(raw)

	for _, tc := range []struct {
		path []string
		want string
	}{
		{[]string{"s"}, "hi"},
		{[]string{"i"}, "7"},
		{[]string{"l"}, "9"},
		{[]string{"d"}, "1.5"},
		{[]string{"b"}, "true"},
		{[]string{"obj", "n"}, "42"}, // dotted path into a sub-document
	} {
		got, err := mongoLookup(doc, tc.path)
		if err != nil || got != tc.want {
			t.Fatalf("lookup %v = %q/%v, want %q", tc.path, got, err, tc.want)
		}
	}

	// Missing path and a non-scalar (sub-document) both error.
	if _, err := mongoLookup(doc, []string{"missing"}); err == nil {
		t.Fatal("a missing path must error")
	}
	if _, err := mongoLookup(doc, []string{"obj"}); err == nil {
		t.Fatal("a non-scalar value must error")
	}
}

func TestParseMongoPipeline(t *testing.T) {
	if _, err := parseMongoPipeline(`[{"$count":"n"}]`); err != nil {
		t.Fatalf("valid pipeline: %v", err)
	}
	if _, err := parseMongoPipeline(`{"$count":"n"}`); err == nil {
		t.Fatal("a non-array pipeline must error")
	}
}

func TestBuildMongoCheckCount(t *testing.T) {
	built, warns := Build(map[string]any{
		"q": map[string]any{
			"type": "mongodb-query", "database": "app", "collection": "jobs",
			"filter": `{"status":"failed"}`, "op": "<", "value": "10",
		},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("count check should build: warns=%v", warns)
	}
	c, ok := built[0].Check.(mongoCheck)
	if !ok || c.mode != "count" || c.collection != "jobs" || c.cfg.Port != 27017 {
		t.Fatalf("built = %+v", built[0].Check)
	}
}

func TestBuildMongoCheckAggregateAndCommand(t *testing.T) {
	built, warns := Build(map[string]any{
		"agg": map[string]any{
			"type": "mongodb-query", "database": "app", "collection": "jobs",
			"pipeline": `[{"$count":"n"}]`, "result": "n", "op": ">", "value": "0",
		},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("aggregate check should build: warns=%v", warns)
	}
	if c := built[0].Check.(mongoCheck); c.mode != "aggregate" || len(c.resultPath) != 1 {
		t.Fatalf("aggregate built = %+v", c)
	}

	// command defaults the database to admin.
	built, warns = Build(map[string]any{
		"cmd": map[string]any{
			"type": "mongodb-query", "command": `{"serverStatus":1}`,
			"result": "connections.current", "op": "<", "value": "5000",
		},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("command check should build: warns=%v", warns)
	}
	c := built[0].Check.(mongoCheck)
	if c.mode != "command" || c.database != "admin" || len(c.resultPath) != 2 {
		t.Fatalf("command built = %+v", c)
	}
}

func TestBuildMongoCheckErrors(t *testing.T) {
	cases := []map[string]any{
		{"type": "mongodb-query", "collection": "j", "database": "app", "op": "<"},                                                    // no value
		{"type": "mongodb-query", "collection": "j", "database": "app", "op": "~~", "value": "1"},                                     // bad op
		{"type": "mongodb-query", "collection": "j", "op": "<", "value": "1"},                                                         // collection without database
		{"type": "mongodb-query", "command": `{"x":1}`, "collection": "j", "database": "app", "op": "<", "value": "1", "result": "x"}, // command + collection
		{"type": "mongodb-query", "database": "app", "collection": "j", "pipeline": `[{"$count":"n"}]`, "op": ">", "value": "0"},      // pipeline without result
		{"type": "mongodb-query", "database": "app", "collection": "j", "filter": `{bad json`, "op": "<", "value": "1"},               // invalid filter JSON
		{"type": "mongodb-query", "op": "<", "value": "1"},                                                                            // nothing to query
	}
	for i, entry := range cases {
		if _, warns := Build(map[string]any{"q": entry}, Deps{DefaultTimeout: time.Second}); len(warns) == 0 {
			t.Fatalf("case %d should warn: %v", i, entry)
		}
	}
}
