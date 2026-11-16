package rules

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
	"sermo/internal/metrics"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

// On a production host every restart for six hours said only "Package update
// is running or cannot be inspected". The block now carries the reading that
// made the guard hold, while Reason stays the guard's own message.
func TestGuardBlockNamesTheProcessThatHeldIt(t *testing.T) {
	ev := &Evaluator{Deps: checks.Deps{ProcessesAny: func(context.Context, []string, string, *regexp.Regexp) process.StateObservation {
		return process.StateObservation{State: process.StateRunning, PID: 1310048, PPID: 1, Program: "emerge"}
	}}}
	rule := Rule{Name: "block-during-package-update", Type: RuleGuard, Blocks: []string{"restart"},
		If:      map[string]any{"not": map[string]any{"process": map[string]any{"cmd": "(^|/)emerge( |$)", "state": "absent"}}},
		Actions: []Action{{Type: ActionBlock, Message: "Package update is running"}}}
	block, err := Guard(t.Context(), []Rule{rule}, "restart", ev)
	if err != nil {
		t.Fatal(err)
	}
	want := GuardBlock{
		Rule:     "block-during-package-update",
		Reason:   "Package update is running",
		Evidence: "process: state running (want absent): pid 1310048 (ppid 1) emerge",
	}
	if block != want {
		t.Fatalf("block = %+v, want %+v", block, want)
	}
	if got := block.MessageWith("expanded"); got != "expanded; observed "+want.Evidence {
		t.Fatalf("message = %q", got)
	}
	if got := (GuardBlock{Rule: "g", Reason: "fixed"}).MessageWith("fixed"); got != "fixed" {
		t.Fatalf("message without evidence = %q", got)
	}
}

// A guard that needed a leaf with no reading (a metric) is not explained by
// its other operands: an or that held through it would name its false
// siblings, and an and would hide that the metric had to hold too. The block
// then carries no evidence rather than a misleading one.
func TestGuardBlockNamesNothingWhenAnUnreadLeafDecided(t *testing.T) {
	metric := map[string]any{ConditionMetric: map[string]any{FieldName: "load1", FieldOp: ">", FieldValue: "4"}}
	for _, tc := range []struct {
		name   string
		backup checks.Result
		node   map[string]any
	}{
		{name: "or", backup: checks.Result{OK: false, Message: "state absent (want running)"},
			node: map[string]any{ConditionOr: []any{metric, map[string]any{ConditionActive: map[string]any{FieldCheck: "backup"}}}}},
		{name: "and", backup: checks.Result{OK: true, Message: "state running (want running): pid 7 (ppid 1) /usr/bin/mariadb-dump"},
			node: map[string]any{ConditionAnd: []any{metric, map[string]any{ConditionActive: map[string]any{FieldCheck: "backup"}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := &Evaluator{
				Cache: map[string]checks.Result{"backup": tc.backup},
				Deps: checks.Deps{Metrics: func(string, string) (metrics.Reading, bool) {
					return metrics.Reading{Absolute: 9, HasAbsolute: true, Ready: true}, true
				}},
			}
			rule := Rule{Name: "busy", Type: RuleGuard, Blocks: []string{"restart"}, If: tc.node}
			block, err := Guard(t.Context(), []Rule{rule}, "restart", ev)
			if err != nil || block.Rule != "busy" || block.Evidence != "" {
				t.Fatalf("block = %+v, err %v; want blocked with no evidence", block, err)
			}
		})
	}
}

// A check the condition reads twice is named once.
func TestGuardBlockNamesEachReadingOnce(t *testing.T) {
	backup := map[string]any{ConditionActive: map[string]any{FieldCheck: "backup"}}
	ev := &Evaluator{Cache: map[string]checks.Result{"backup": {OK: true, Message: "state running (want running)"}}}
	rule := Rule{Name: "backup", Type: RuleGuard, Blocks: []string{"restart"},
		If: map[string]any{ConditionAnd: []any{backup, map[string]any{ConditionOr: []any{backup}}}}}
	block, err := Guard(t.Context(), []Rule{rule}, "restart", ev)
	if err != nil || block.Evidence != "backup: state running (want running)" {
		t.Fatalf("block = %+v, err %v", block, err)
	}
}

// Every inline leaf a guard can hold on is read back after a real evaluation:
// should eval change how it memoizes one, its evidence goes empty here.
func TestGuardBlockNamesEveryInlineLeaf(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "migrating")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ev := &Evaluator{Deps: checks.Deps{
		DefaultTimeout: time.Second,
		Runner:         execxtest.Fixed(execx.Result{ExitCode: 0}, nil),
		Status:         func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil },
		ProcessesAny: func(context.Context, []string, string, *regexp.Regexp) process.StateObservation {
			return process.StateObservation{State: process.StateRunning, PID: 9, PPID: 1, Program: "emerge"}
		},
	}}
	for _, leaf := range []map[string]any{
		{ConditionFile: map[string]any{FieldPath: marker}},
		{ConditionCommand: map[string]any{"command": []any{"/usr/local/bin/busy"}}},
		{ConditionService: map[string]any{FieldState: "active"}},
		{ConditionProcess: map[string]any{"cmd": "emerge", FieldState: process.StateRunning}},
		{ConditionActive: map[string]any{"command": map[string]any{"command": []any{"/usr/local/bin/busy"}}}},
	} {
		block, err := Guard(t.Context(), []Rule{{Name: "g", Type: RuleGuard, Blocks: []string{"restart"}, If: leaf}}, "restart", ev)
		if err != nil || block.Rule != "g" || block.Evidence == "" {
			t.Fatalf("leaf %v: block = %+v, err %v; want blocked with its reading", leaf, block, err)
		}
	}
}

// Only the readings that decide the condition are named: the true operand of
// an or, every operand of an and that holds, the false operands of a failing
// and under not. A leaf with no result this cycle (a metric) or a reading the
// guard cannot rely on (unavailable) names nothing.
func TestExplainNamesDecidingReadings(t *testing.T) {
	results := map[string]checks.Result{
		"backup":  {Check: "backup", OK: true, Message: "state running (want running): pid 7 (ppid 1) /usr/bin/mariadb-dump"},
		"restore": {Check: "restore", OK: false, Message: "state absent (want running)"},
		"tcp":     {Check: "tcp", OK: false, Message: "connection refused"},
		"lost":    {Check: "lost", OK: false, Unavailable: true, Message: "timeout"},
	}
	active := func(name string) map[string]any {
		return map[string]any{ConditionActive: map[string]any{FieldCheck: name}}
	}
	failed := func(name string) map[string]any {
		return map[string]any{ConditionFailed: map[string]any{FieldCheck: name}}
	}
	metric := map[string]any{ConditionMetric: map[string]any{FieldName: "load1", FieldOp: ">", FieldValue: "4"}}
	backup := "backup: " + results["backup"].Message
	for _, tc := range []struct {
		name string
		node map[string]any
		want []string
	}{
		{name: "or", node: map[string]any{ConditionOr: []any{active("restore"), active("backup")}}, want: []string{backup}},
		{name: "and", node: map[string]any{ConditionAnd: []any{active("backup"), map[string]any{ConditionNot: active("tcp")}}},
			want: []string{backup, "tcp: connection refused"}},
		{name: "not and", node: map[string]any{ConditionNot: map[string]any{ConditionAnd: []any{active("backup"), active("restore")}}},
			want: []string{"restore: state absent (want running)"}},
		{name: "unknown leaf", node: map[string]any{ConditionAnd: []any{metric, active("backup")}}, want: []string{backup}},
		{name: "unavailable sibling", node: map[string]any{ConditionOr: []any{failed("lost"), active("backup")}}, want: []string{backup}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := &Evaluator{Cache: results, FailOnUnavailable: true}
			if _, _, got := ev.explain(tc.node); !slices.Equal(got, tc.want) {
				t.Fatalf("readings = %q, want %q", got, tc.want)
			}
		})
	}
}

// Inline probes are read back from the cycle memo under the key the evaluator
// stored them with, so the guard names them by probe type.
func TestExplainReadsInlineProbes(t *testing.T) {
	rpcbind := map[string]any{"rpcbind": map[string]any{"host": "127.0.0.1"}}
	marker := map[string]any{FieldPath: "/run/migrating", FieldExists: false}
	ev := &Evaluator{memo: map[string]checks.Result{
		inlineMemoKey("rpcbind", rpcbind):    {OK: false, Message: "connection refused"},
		inlineMemoKey(ConditionFile, marker): {OK: false, Message: "/run/migrating absent"},
	}}
	node := map[string]any{ConditionAnd: []any{
		map[string]any{ConditionNot: map[string]any{ConditionActive: rpcbind}},
		map[string]any{ConditionFile: marker},
	}}
	holds, known, got := ev.explain(node)
	want := []string{"rpcbind: connection refused", "file: /run/migrating absent"}
	if !holds || !known || !slices.Equal(got, want) {
		t.Fatalf("explain = %v %v %q, want true true %q", holds, known, got, want)
	}
}
