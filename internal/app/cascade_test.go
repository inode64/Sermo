package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"sermo/internal/operation"
)

func TestOrderedGroupDependencyOrder(t *testing.T) {
	// a -> [b, c]; b -> [d]
	graph := map[string][]string{"a": {"b", "c"}, "b": {"d"}}
	lookup := func(s string) []string { return graph[s] }

	start := OrderedGroup("a", "restart", lookup, map[string]bool{}, 0)
	// pre-order: primary first, then dependents (a, b, d, c).
	if got := start; !eq(got, []string{"a", "b", "d", "c"}) {
		t.Fatalf("start/restart order = %v, want [a b d c]", got)
	}
	stop := OrderedGroup("a", "stop", lookup, map[string]bool{}, 0)
	// post-order: dependents first, primary last (d, b, c, a).
	if got := stop; !eq(got, []string{"d", "b", "c", "a"}) {
		t.Fatalf("stop order = %v, want [d b c a]", got)
	}
}

func TestOrderedGroupCutsCycle(t *testing.T) {
	graph := map[string][]string{"a": {"b"}, "b": {"a"}} // cycle
	lookup := func(s string) []string { return graph[s] }
	got := OrderedGroup("a", "restart", lookup, map[string]bool{}, 0)
	if !eq(got, []string{"a", "b"}) {
		t.Fatalf("cycle must terminate with each once, got %v", got)
	}
}

func TestCascaderRunReportsTargetsReturnsPrimary(t *testing.T) {
	var ops []string
	op := func(_ context.Context, svc, action string) (operation.Result, error) {
		ops = append(ops, action+" "+svc)
		st := operation.ResultOK
		if svc == "primary" {
			return operation.Result{Service: svc, Status: st, Message: "primary-msg"}, nil
		}
		return operation.Result{Service: svc, Status: st}, nil
	}
	var events []Event
	c := cascader{config: CascadeConfig{
		Operate: op,
		Lookup:  func(s string) []string { return map[string][]string{"primary": {"dep"}}[s] },
		Emit:    func(e Event) { events = append(events, e) },
	}}
	res, err := c.run(context.Background(), "primary", "restart")
	if err != nil {
		t.Fatalf("run error = %v", err)
	}
	if res.Message != "primary-msg" {
		t.Fatalf("run must return the primary's result, got %+v", res)
	}
	if !eq(ops, []string{"restart primary", "restart dep"}) {
		t.Fatalf("ops = %v, want [restart primary, restart dep]", ops)
	}
	if len(events) != 1 || events[0].Kind != "cascade" || events[0].Service != "dep" {
		t.Fatalf("expected one cascade event for dep, got %+v", events)
	}
}

func TestCascaderDowngradesPrimaryWhenAdditionalFails(t *testing.T) {
	op := func(_ context.Context, svc, action string) (operation.Result, error) {
		if svc == "dep" {
			return operation.Result{Service: svc, Status: operation.ResultFailed, Message: "stop failed"}, nil
		}
		return operation.Result{Service: svc, Status: operation.ResultOK, Message: "restart ok"}, nil
	}
	c := cascader{config: CascadeConfig{
		Operate: op,
		Lookup:  func(s string) []string { return map[string][]string{"primary": {"dep"}}[s] },
		Emit:    func(Event) {},
	}}
	res, _ := c.run(context.Background(), "primary", "restart")
	if res.Status != operation.ResultFailed {
		t.Fatalf("status = %s, want failed when cascade target fails", res.Status)
	}
	if !strings.Contains(res.Message, "cascade target failed") {
		t.Fatalf("message = %q, want cascade failure noted", res.Message)
	}
}

func TestCascaderRetriesBlockedTarget(t *testing.T) {
	calls := 0
	op := func(_ context.Context, svc, action string) (operation.Result, error) {
		if svc == "dep" {
			calls++
			if calls == 1 {
				return operation.Result{Service: svc, Status: operation.ResultBlocked}, nil
			}
		}
		return operation.Result{Service: svc, Status: operation.ResultOK}, nil
	}
	c := cascader{
		config: CascadeConfig{
			Operate: op,
			Lookup:  func(s string) []string { return map[string][]string{"primary": {"dep"}}[s] },
			Emit:    func(Event) {},
		},
		sleep: func(time.Duration) {}, // no-op backoff in tests
	}
	_, _ = c.run(context.Background(), "primary", "restart")
	if calls != 2 {
		t.Fatalf("a blocked target must be retried once, got %d calls", calls)
	}
}

func TestCascaderDoesNotRetryAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	c := cascader{config: CascadeConfig{
		Operate: func(_ context.Context, svc, _ string) (operation.Result, error) {
			if svc == "dep" {
				calls++
				cancel()
				return operation.Result{Service: svc, Status: operation.ResultBlocked}, nil
			}
			return operation.Result{Service: svc, Status: operation.ResultOK}, nil
		},
		Lookup: func(s string) []string { return map[string][]string{"primary": {"dep"}}[s] },
	}}
	_, _ = c.run(ctx, "primary", "restart")
	if calls != 1 {
		t.Fatalf("cancelled blocked target calls = %d, want 1", calls)
	}
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
