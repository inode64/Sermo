package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/operation"
	"sermo/internal/rules"
)

func TestSupervisorRenewalAcrossRestartRules(t *testing.T) {
	group := &supervisorRenewals{owners: make(map[string]string)}
	stale := checks.Result{Check: "pair", ActionBlocked: true, Data: map[string]any{checks.DataKeyReplacedSupervisor: "/bin/supervisor"}}
	ready := checks.Result{Check: "pair", OK: true}
	newMember := func(name, rule string) (*workerHarness, *Worker) {
		h := &workerHarness{cache: map[string]checks.Result{"pair": stale, "trigger": {Check: "trigger"}}, opResult: operation.Result{Status: operation.ResultOK}}
		w := h.worker(remediationTree(rule, "trigger", "restart"), rules.Policy{Cooldown: time.Minute}, nil)
		w.Service = name
		w.renewal = &serviceRenewal{group: group, probes: map[string]checks.Built{"pair": {}}}
		return h, w
	}
	first, a := newMember("first", "replace-binary")
	second, b := newMember("second", "other-rule")
	first.opResult.Status = operation.ResultPreflightFailed
	a.RunCycle(t.Context())
	b.RunCycle(t.Context())
	if len(second.ops) != 1 {
		t.Fatal("preflight refusal held the group")
	}

	first, a = newMember("first", "another-rule")
	a.RunCycle(t.Context())
	if len(first.ops) != 0 || !a.State.LastActionAt.IsZero() {
		t.Fatal("overlapping restart or cooldown charged")
	}

	second.cache["pair"] = checks.Result{Check: "pair", Unavailable: true}
	b.RunCycle(t.Context())
	a.RunCycle(t.Context())
	if len(first.ops) != 0 {
		t.Fatal("unavailable observation released the group")
	}

	down, c := newMember("absent", "worker-missing")
	down.cache["pair"] = checks.Result{Check: "pair"}
	c.RunCycle(t.Context())
	if len(down.ops) != 1 {
		t.Fatal("proven absence waited behind a renewal")
	}

	saved := captureWorkerState([]*Worker{a, b})
	group = &supervisorRenewals{owners: make(map[string]string)}
	a.renewal.group, b.renewal.group = group, group
	applyWorkerState([]*Worker{a, b}, saved)
	a.RunCycle(t.Context())
	if len(first.ops) != 0 {
		t.Fatal("reload released an unverified replica")
	}
	second.cache["pair"] = ready
	b.cycleRan = map[string]bool{"pair": false}
	b.RunCycle(t.Context())
	a.RunCycle(t.Context())
	if len(first.ops) != 0 {
		t.Fatal("cached readiness released the replica")
	}
	b.cycleRan["pair"] = true
	b.RunCycle(t.Context())
	a.RunCycle(t.Context())
	if len(first.ops) != 1 {
		t.Fatalf("expected one restart after peer readiness, got %v", first.ops)
	}
}

func TestSupervisorRenewalDuringConcurrentCascade(t *testing.T) {
	group := &supervisorRenewals{owners: make(map[string]string)}
	pair := stubCheck{name: "pair", data: map[string]any{checks.DataKeyReplacedSupervisor: "/bin/supervisor"}}
	makeService := func(name string) *Worker {
		return &Worker{Service: name, renewal: &serviceRenewal{group: group, probes: map[string]checks.Built{"pair": {Check: pair}}}}
	}
	a, b := makeService("a"), makeService("b")
	started, finish := make(chan struct{}), make(chan struct{})
	a.Operate = func(ctx context.Context, _ string) operation.Result {
		close(started)
		select {
		case <-ctx.Done():
		case <-finish:
		}
		return operation.Result{Status: operation.ResultFailed}
	}
	b.Operate = func(context.Context, string) operation.Result { return operation.Result{Status: operation.ResultOK} }
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan operation.Result, 1)
	go func() { done <- a.operateCascadeTarget(ctx, "restart") }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if got := b.operateCascadeTarget(ctx, "restart"); got.Status != operation.ResultBlocked {
		t.Fatalf("overlap: %+v", got)
	}
	early := time.Now()
	close(finish)
	if got := <-done; got.Status != operation.ResultFailed {
		t.Fatalf("failed operation: %+v", got)
	}
	ready := map[string]checks.Result{"pair": {OK: true}}
	a.renewal.observe("a", ready, nil, early)
	if got := b.operateCascadeTarget(ctx, "restart"); got.Status != operation.ResultBlocked {
		t.Fatalf("observation started before completion released group: %+v", got)
	}
	a.renewal.observe("a", ready, nil, time.Now())
	if got := b.operateCascadeTarget(ctx, "restart"); !got.OK() {
		t.Fatalf("ready peer: %+v", got)
	}
}
