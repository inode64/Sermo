package rules

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"sermo/internal/checks"
	"sermo/internal/execx"
	"sermo/internal/metrics"
)

func cache(results map[string]bool) map[string]checks.Result {
	out := map[string]checks.Result{}
	for name, ok := range results {
		out[name] = checks.Result{Check: name, OK: ok}
	}
	return out
}

func evalNode(t *testing.T, ev *Evaluator, node map[string]any) bool {
	t.Helper()
	got, err := ev.Eval(context.Background(), node)
	if err != nil {
		t.Fatalf("Eval(%v) error = %v", node, err)
	}
	return got
}

func TestEvalFailedActiveOverCache(t *testing.T) {
	ev := &Evaluator{Cache: cache(map[string]bool{"http": false, "backup-flag": true})}

	if !evalNode(t, ev, map[string]any{"failed": map[string]any{"check": "http"}}) {
		t.Error("failed http (OK=false) should be true")
	}
	if evalNode(t, ev, map[string]any{"active": map[string]any{"check": "http"}}) {
		t.Error("active http (OK=false) should be false")
	}
	if !evalNode(t, ev, map[string]any{"active": map[string]any{"check": "backup-flag"}}) {
		t.Error("active backup-flag (OK=true) should be true")
	}
}

func TestEvalAndOrNot(t *testing.T) {
	ev := &Evaluator{Cache: cache(map[string]bool{"a": false, "b": true})}
	failedA := map[string]any{"failed": map[string]any{"check": "a"}} // true
	failedB := map[string]any{"failed": map[string]any{"check": "b"}} // false

	if !evalNode(t, ev, map[string]any{"or": []any{failedA, failedB}}) {
		t.Error("or(true,false) should be true")
	}
	if evalNode(t, ev, map[string]any{"and": []any{failedA, failedB}}) {
		t.Error("and(true,false) should be false")
	}
	if !evalNode(t, ev, map[string]any{"not": failedB}) {
		t.Error("not(false) should be true")
	}
}

func TestEvalUnknownCheckIsError(t *testing.T) {
	ev := &Evaluator{Cache: cache(nil)}
	if _, err := ev.Eval(context.Background(), map[string]any{"failed": map[string]any{"check": "nope"}}); err == nil {
		t.Fatal("unknown check reference should error")
	}
}

func TestEvalInlineTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())

	ev := &Evaluator{Deps: checks.Deps{DefaultTimeout: 0}}
	active := map[string]any{"active": map[string]any{"tcp": map[string]any{"host": "127.0.0.1", "port": portStr}}}
	if !evalNode(t, ev, active) {
		t.Error("active inline tcp to an open port should be true")
	}
	failed := map[string]any{"failed": map[string]any{"tcp": map[string]any{"host": "127.0.0.1", "port": portStr}}}
	if evalNode(t, ev, failed) {
		t.Error("failed inline tcp to an open port should be false")
	}
}

type fakeRunner struct{ exit int }

func (r fakeRunner) Run(context.Context, string, ...string) (execx.Result, error) {
	return execx.Result{ExitCode: r.exit}, nil
}

func TestEvalInlineCommand(t *testing.T) {
	ev := &Evaluator{Deps: checks.Deps{Runner: fakeRunner{exit: 0}}}
	node := map[string]any{"command": map[string]any{"command": []any{"can-restart"}, "expect_exit": 0}}
	if !evalNode(t, ev, node) {
		t.Error("command exit 0 (expect 0) should be true")
	}

	ev = &Evaluator{Deps: checks.Deps{Runner: fakeRunner{exit: 1}}}
	if evalNode(t, ev, node) {
		t.Error("command exit 1 (expect 0) should be false")
	}
}

func TestEvalFileCondition(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "lock")
	if err := os.WriteFile(present, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev := &Evaluator{}

	if !evalNode(t, ev, map[string]any{"file": map[string]any{"path": present, "exists": true}}) {
		t.Error("file exists:true should match a present file")
	}
	if evalNode(t, ev, map[string]any{"file": map[string]any{"path": present, "exists": false}}) {
		t.Error("file exists:false should not match a present file")
	}
	absent := filepath.Join(dir, "missing")
	if !evalNode(t, ev, map[string]any{"file": map[string]any{"path": absent, "exists": false}}) {
		t.Error("file exists:false should match an absent file")
	}
}

func TestEvalInlineProbeMemoized(t *testing.T) {
	runs := &countingRunner{}
	ev := &Evaluator{Deps: checks.Deps{Runner: runs}}
	node := map[string]any{
		"and": []any{
			map[string]any{"active": map[string]any{"command": map[string]any{"command": []any{"x"}, "expect_exit": 0}}},
			map[string]any{"active": map[string]any{"command": map[string]any{"command": []any{"x"}, "expect_exit": 0}}},
		},
	}
	evalNode(t, ev, node)
	if runs.n != 1 {
		t.Fatalf("identical inline probe ran %d times, want 1 (memoized)", runs.n)
	}
}

type countingRunner struct{ n int }

func (r *countingRunner) Run(context.Context, string, ...string) (execx.Result, error) {
	r.n++
	return execx.Result{ExitCode: 0}, nil
}

func TestEvalMetricCondition(t *testing.T) {
	source := func(scope, name string) (metrics.Reading, bool) {
		if scope == "service" && name == "cpu" {
			return metrics.Reading{Percent: 85, HasPercent: true, Ready: true}, true
		}
		return metrics.Reading{}, false
	}
	ev := &Evaluator{Deps: checks.Deps{Metrics: source}}

	hot := map[string]any{"metric": map[string]any{"name": "cpu", "op": ">", "value": "80%"}}
	if !evalNode(t, ev, hot) {
		t.Error("cpu 85% > 80% should be true")
	}
	cool := map[string]any{"metric": map[string]any{"name": "cpu", "op": "<", "value": "80%"}}
	if evalNode(t, ev, cool) {
		t.Error("cpu 85% < 80% should be false")
	}
}

func TestEvalMetricNotReadyOrAbsentIsFalse(t *testing.T) {
	// Not ready -> false (never fire remediation on an uncomputed rate).
	ev := &Evaluator{Deps: checks.Deps{Metrics: func(string, string) (metrics.Reading, bool) {
		return metrics.Reading{Percent: 99, HasPercent: true, Ready: false}, true
	}}}
	if evalNode(t, ev, map[string]any{"metric": map[string]any{"name": "cpu", "op": ">", "value": "1%"}}) {
		t.Error("not-ready metric must be false")
	}

	// No source at all -> false.
	ev = &Evaluator{}
	if evalNode(t, ev, map[string]any{"metric": map[string]any{"name": "cpu", "op": ">", "value": "1%"}}) {
		t.Error("absent metric source must be false")
	}
}

func TestParseRules(t *testing.T) {
	tree := map[string]any{
		"rules": map[string]any{
			"block-during-backup": map[string]any{
				"type":   "guard",
				"blocks": []any{"restart", "stop"},
				"if":     map[string]any{"active": map[string]any{"check": "backup-flag"}},
				"then":   map[string]any{"action": "block", "message": "backup running"},
			},
			"disabled-rule": map[string]any{
				"enabled": false,
				"type":    "guard",
				"if":      map[string]any{"active": map[string]any{"check": "x"}},
				"then":    map[string]any{"action": "block"},
			},
			"no-then": map[string]any{
				"type": "remediation",
				"if":   map[string]any{"failed": map[string]any{"check": "http"}},
			},
		},
	}
	ruleSet, warnings := ParseRules(tree)
	if len(ruleSet) != 1 {
		t.Fatalf("parsed %d rules, want 1: %+v", len(ruleSet), ruleSet)
	}
	r := ruleSet[0]
	if r.Name != "block-during-backup" || r.Type != RuleGuard || len(r.Blocks) != 2 {
		t.Fatalf("rule = %+v", r)
	}
	if r.Then.Type != ActionBlock || r.Then.Message != "backup running" {
		t.Fatalf("action = %+v", r.Then)
	}
	if len(warnings) != 1 { // no-then warns; disabled is silent
		t.Fatalf("warnings = %v, want 1", warnings)
	}
}

func TestGuardBlocksMatchingAction(t *testing.T) {
	tree := map[string]any{
		"rules": map[string]any{
			"block-during-backup": map[string]any{
				"type":   "guard",
				"blocks": []any{"restart", "stop"},
				"if":     map[string]any{"active": map[string]any{"check": "backup-flag"}},
				"then":   map[string]any{"action": "block", "message": "backup running"},
			},
		},
	}
	ruleSet, _ := ParseRules(tree)
	ev := &Evaluator{Cache: cache(map[string]bool{"backup-flag": true})}

	blocked, reason, err := Guard(context.Background(), ruleSet, "restart", ev)
	if err != nil {
		t.Fatalf("Guard error = %v", err)
	}
	if !blocked || reason != "backup running" {
		t.Fatalf("restart should be blocked: blocked=%v reason=%q", blocked, reason)
	}

	// start is not in blocks -> not blocked.
	blocked, _, _ = Guard(context.Background(), ruleSet, "start", ev)
	if blocked {
		t.Fatal("start is not in blocks; must not be blocked")
	}

	// When the backup is not active, restart is allowed.
	ev = &Evaluator{Cache: cache(map[string]bool{"backup-flag": false})}
	blocked, _, _ = Guard(context.Background(), ruleSet, "restart", ev)
	if blocked {
		t.Fatal("restart must be allowed when backup-flag is inactive")
	}
}

func TestGuardIgnoresNonGuardRules(t *testing.T) {
	tree := map[string]any{
		"rules": map[string]any{
			"restart-if-down": map[string]any{
				"type": "remediation",
				"if":   map[string]any{"failed": map[string]any{"check": "http"}},
				"then": map[string]any{"action": "restart"},
			},
		},
	}
	ruleSet, _ := ParseRules(tree)
	ev := &Evaluator{Cache: cache(map[string]bool{"http": false})}

	blocked, _, err := Guard(context.Background(), ruleSet, "restart", ev)
	if err != nil {
		t.Fatalf("Guard error = %v", err)
	}
	if blocked {
		t.Fatal("a remediation rule must never block an action")
	}
}
