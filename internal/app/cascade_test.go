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
	op := func(_ context.Context, svc, action string) operation.Result {
		ops = append(ops, action+" "+svc)
		st := operation.ResultOK
		if svc == "primary" {
			return operation.Result{Service: svc, Status: st, Message: "primary-msg"}
		}
		return operation.Result{Service: svc, Status: st}
	}
	var events []Event
	c := cascader{
		op:     op,
		lookup: func(s string) []string { return map[string][]string{"primary": {"dep"}}[s] },
		emit:   func(e Event) { events = append(events, e) },
	}
	res := c.run(context.Background(), "primary", "restart")
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
	op := func(_ context.Context, svc, action string) operation.Result {
		if svc == "dep" {
			return operation.Result{Service: svc, Status: operation.ResultFailed, Message: "stop failed"}
		}
		return operation.Result{Service: svc, Status: operation.ResultOK, Message: "restart ok"}
	}
	c := cascader{
		op:     op,
		lookup: func(s string) []string { return map[string][]string{"primary": {"dep"}}[s] },
		emit:   func(Event) {},
	}
	res := c.run(context.Background(), "primary", "restart")
	if res.Status != operation.ResultFailed {
		t.Fatalf("status = %s, want failed when cascade target fails", res.Status)
	}
	if !strings.Contains(res.Message, "cascade target failed") {
		t.Fatalf("message = %q, want cascade failure noted", res.Message)
	}
}

func TestCascaderRetriesBlockedTarget(t *testing.T) {
	calls := 0
	op := func(_ context.Context, svc, action string) operation.Result {
		if svc == "dep" {
			calls++
			if calls == 1 {
				return operation.Result{Service: svc, Status: operation.ResultBlocked}
			}
		}
		return operation.Result{Service: svc, Status: operation.ResultOK}
	}
	c := cascader{
		op:     op,
		lookup: func(s string) []string { return map[string][]string{"primary": {"dep"}}[s] },
		emit:   func(Event) {},
		sleep:  func(time.Duration) {}, // no-op backoff in tests
	}
	c.run(context.Background(), "primary", "restart")
	if calls != 2 {
		t.Fatalf("a blocked target must be retried once, got %d calls", calls)
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
