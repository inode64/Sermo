package app

import (
	"os"
	"testing"

	"sermo/internal/checks"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ranChecks(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func TestApplyGatesRequires(t *testing.T) {
	w := &Worker{Gates: map[string]CheckGate{
		"query": {Requires: []string{"tcp"}},
	}}
	// dependency failing -> query skipped (not failed)
	cache := map[string]checks.Result{
		"tcp":   {Check: "tcp", OK: false},
		"query": {Check: "query", OK: false, Message: "connection refused"},
	}
	w.cycleRan = ranChecks("tcp", "query")
	w.applyGates(cache)
	q := cache["query"]
	if !q.Skipped || !q.OK {
		t.Fatalf("query should be skipped+OK when its dependency fails: %+v", q)
	}
	// tcp itself is still failing, so the service is down — but query no longer
	// double-counts: requiredChecksOK reflects only the genuinely-failing checks.
	_ = requiredChecksOK(cache)

	// dependency OK -> query keeps its (failing) result
	cache2 := map[string]checks.Result{
		"tcp":   {Check: "tcp", OK: true},
		"query": {Check: "query", OK: false, Message: "boom"},
	}
	w.cycleRan = ranChecks("tcp", "query")
	w.applyGates(cache2)
	if cache2["query"].Skipped || cache2["query"].OK {
		t.Fatalf("query must keep its real result when the dependency is OK: %+v", cache2["query"])
	}
}

func TestArtifactChangedFuncSharesWorkerBaseline(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/lib.so"
	writeFile(t, path, "v1")

	baseline := map[string]string{}
	changed := ArtifactChangedFunc(baseline)
	w := &Worker{libBaseline: baseline}

	if c, _ := changed(path); c {
		t.Fatal("first observation must adopt baseline, not report changed")
	}
	if c, _ := w.changed(path); c {
		t.Fatal("worker must share the same adopted baseline")
	}

	writeFile(t, path, "v2")
	if c, _ := changed(path); !c {
		t.Fatal("shared baseline must see the file change")
	}
}

func TestApplyGatesSkipWhenChanged(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/conf"
	writeFile(t, path, "v1")

	w := &Worker{Gates: map[string]CheckGate{
		"probe": {SkipWhenChanged: []string{path}},
	}}
	// first observation primes the baseline -> not skipped
	cache := map[string]checks.Result{"probe": {Check: "probe", OK: true}}
	w.applyGates(cache)
	if cache["probe"].Skipped {
		t.Fatal("first cycle must prime the baseline, not skip")
	}

	// change the file -> probe skipped
	writeFile(t, path, "v2-bigger-content")
	cache2 := map[string]checks.Result{"probe": {Check: "probe", OK: false, Message: "down"}}
	w.applyGates(cache2)
	if !cache2["probe"].Skipped {
		t.Fatalf("probe must be skipped after the watched file changed: %+v", cache2["probe"])
	}
}

func TestApplyGatesRequiresIgnoresStaleDependency(t *testing.T) {
	w := &Worker{Gates: map[string]CheckGate{
		"query": {Requires: []string{"tcp"}},
	}}
	// tcp failed when last evaluated, but only query ran this cycle.
	cache := map[string]checks.Result{
		"tcp":   {Check: "tcp", OK: false},
		"query": {Check: "query", OK: false, Message: "boom"},
	}
	w.cycleRan = ranChecks("query")
	w.applyGates(cache)
	if cache["query"].Skipped || cache["query"].OK {
		t.Fatalf("query must keep its real result when the dependency was not re-evaluated: %+v", cache["query"])
	}
}

func TestGatedChecksDue(t *testing.T) {
	for _, tc := range []struct {
		name      string
		tcpOK     bool
		wantRerun bool
	}{
		{"skip clears when dependency recovers", true, true},
		{"still skipped when dependency failing", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &Worker{
				Gates:    map[string]CheckGate{"query": {Requires: []string{"tcp"}}},
				cycleRan: ranChecks("tcp"),
			}
			cache := map[string]checks.Result{
				"tcp":   {Check: "tcp", OK: tc.tcpOK},
				"query": {Check: "query", OK: true, Skipped: true, Message: "skipped: requires check tcp"},
			}
			built := []checks.Built{{Check: stubCheck{name: "query"}}}
			extra := w.gatedChecksDue(built, cache)
			if tc.wantRerun {
				if len(extra) != 1 || extra[0].Check.Name() != "query" {
					t.Fatalf("extra = %+v, want query re-run", extra)
				}
			} else if len(extra) != 0 {
				t.Fatal("dependency still failing — query must stay deferred")
			}
		})
	}
}

func TestParseCheckGates(t *testing.T) {
	tree := map[string]any{"checks": map[string]any{
		"tcp":   map[string]any{"type": "tcp"},
		"query": map[string]any{"type": "command", "requires": []any{"tcp"}, "skip_when_changed": []any{"/etc/my.cnf"}},
		"plain": map[string]any{"type": "http"},
	}}
	gates := parseCheckGates(tree)
	if len(gates) != 1 {
		t.Fatalf("only `query` is gated: %+v", gates)
	}
	g := gates["query"]
	if len(g.Requires) != 1 || g.Requires[0] != "tcp" || len(g.SkipWhenChanged) != 1 {
		t.Fatalf("query gate parsed wrong: %+v", g)
	}
	if parseCheckGates(map[string]any{"checks": map[string]any{"a": map[string]any{"type": "tcp"}}}) != nil {
		t.Fatal("a service with no gated checks should yield nil")
	}
}
