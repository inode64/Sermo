package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sermo/internal/operation"
)

// TestWireCascadeAcknowledgesTargetChanges: when A cascades a restart to B,
// B runs the upgraded library afterwards too, so B's own `changed:` rule must
// not restart it a second time.
func TestWireCascadeAcknowledgesTargetChanges(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "libshared.so")
	writeFile(t, lib, "upgraded")
	operate := func(_ context.Context, action string) operation.Result {
		return operation.Result{Action: action, Status: operation.ResultOK}
	}
	tests := []struct {
		name        string
		action      string
		status      operation.ResultStatus
		wantChanged bool
	}{
		{name: "successful restart acknowledges", action: "restart", status: operation.ResultOK, wantChanged: false},
		{name: "failed restart keeps the change", action: "restart", status: operation.ResultFailed, wantChanged: true},
		{name: "stop does not acknowledge", action: "stop", status: operation.ResultOK, wantChanged: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Worker{Service: "a", Operate: operate, libBaseline: NewArtifactBaseline()}
			b := &Worker{Service: "b", libBaseline: testArtifactBaseline(map[string]string{lib: "before-upgrade"}),
				Operate: func(_ context.Context, action string) operation.Result {
					return operation.Result{Action: action, Status: tt.status}
				}}
			wireCascade([]*Worker{a, b}, map[string][]string{"a": {"b"}}, Deps{})

			a.Cascade(t.Context(), tt.action)

			if changed, err := b.changed(lib); err != nil || changed != tt.wantChanged {
				t.Fatalf("b changed(%s) = %t, %v; want %t", lib, changed, err, tt.wantChanged)
			}
		})
	}
}

func TestOrderedGroupDependencyOrder(t *testing.T) {
	// a -> [b, c]; b -> [d]
	graph := map[string][]string{"a": {"b", "c"}, "b": {"d"}}
	lookup := func(s string) []string { return graph[s] }

	start := orderedGroup("a", "restart", lookup, map[string]bool{}, 0)
	// pre-order: primary first, then dependents (a, b, d, c).
	if got := start; !eq(got, []string{"a", "b", "d", "c"}) {
		t.Fatalf("start/restart order = %v, want [a b d c]", got)
	}
	stop := orderedGroup("a", "stop", lookup, map[string]bool{}, 0)
	// post-order: dependents first, primary last (d, b, c, a).
	if got := stop; !eq(got, []string{"d", "b", "c", "a"}) {
		t.Fatalf("stop order = %v, want [d b c a]", got)
	}
}

func TestOrderedGroupCutsCycle(t *testing.T) {
	graph := map[string][]string{"a": {"b"}, "b": {"a"}} // cycle
	lookup := func(s string) []string { return graph[s] }
	got := orderedGroup("a", "restart", lookup, map[string]bool{}, 0)
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

// Downgraded hears the root's final result only when the cascade turned its
// own success into a failure.
func TestCascaderReportsADowngradedPrimaryOnce(t *testing.T) {
	for _, tc := range []struct {
		name          string
		primary, dep  operation.ResultStatus
		wantDowngrade bool
	}{
		{name: "target failed", primary: operation.ResultOK, dep: operation.ResultFailed, wantDowngrade: true},
		{name: "target ok", primary: operation.ResultOK, dep: operation.ResultOK},
		{name: "target blocked", primary: operation.ResultOK, dep: operation.ResultBlocked},
		{name: "primary failed on its own", primary: operation.ResultFailed, dep: operation.ResultFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			op := func(_ context.Context, svc, action string) (operation.Result, error) {
				st := tc.primary
				if svc == "dep" {
					st = tc.dep
				}
				return operation.Result{Service: svc, Action: action, Status: st}, nil
			}
			var downgraded []operation.Result
			c := cascader{
				config: CascadeConfig{
					Operate:    op,
					Lookup:     func(s string) []string { return map[string][]string{"primary": {"dep"}}[s] },
					Emit:       func(Event) {},
					Downgraded: func(r operation.Result) { downgraded = append(downgraded, r) },
				},
				sleep: func(time.Duration) {},
			}
			res, _ := c.run(context.Background(), "primary", "restart")
			if got := len(downgraded) == 1; got != tc.wantDowngrade || len(downgraded) > 1 {
				t.Fatalf("downgraded = %+v, want one report: %v", downgraded, tc.wantDowngrade)
			}
			if tc.wantDowngrade && (downgraded[0].Service != "primary" || downgraded[0].Status != res.Status) {
				t.Fatalf("downgraded = %+v, want the returned primary %+v", downgraded[0], res)
			}
		})
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
