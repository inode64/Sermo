package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/locks"
	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
	"sermo/internal/web"
)

type fakeManager struct{ stopped *bool }

func TestWebActionsRejectUnavailableServicesWithOneEvent(t *testing.T) {
	for _, action := range []struct {
		name string
		run  func(*WebBackend) web.ActionResult
	}{
		{name: "start", run: func(b *WebBackend) web.ActionResult {
			return b.Operate(t.Context(), "web", string(rules.ActionStart), web.OperateOpts{})
		}},
		{name: "close SSH session", run: func(b *WebBackend) web.ActionResult {
			return b.CloseSSHSession(t.Context(), "web", web.SSHSession{})
		}},
		{name: "close terminal session", run: func(b *WebBackend) web.ActionResult {
			return b.CloseTerminalSession(t.Context(), "web", web.TerminalSession{})
		}},
		{name: "close empty terminal session", run: func(b *WebBackend) web.ActionResult {
			return b.CloseEmptyTerminalSession(t.Context(), "web", "sessions")
		}},
		{name: "close SMB connection", run: func(b *WebBackend) web.ActionResult {
			return b.CloseSMBSession(t.Context(), "web", "sessions", "identity")
		}},
		{name: string(rules.ActionKillQuery), run: func(b *WebBackend) web.ActionResult {
			return b.KillDBQuery(t.Context(), "web", web.DBQueryKillRequest{})
		}},
	} {
		for _, disabled := range []bool{false, true} {
			state, message := "missing", "unknown service web"
			var entry *webEntry
			if disabled {
				state, message = "disabled", "service web is disabled in configuration"
				entry = &webEntry{disabled: true, engine: operation.Engine{ConfigError: errors.New("operation engine must not run")}}
			}
			t.Run(action.name+"/"+state, func(t *testing.T) {
				var events []Event
				b := &WebBackend{entries: map[string]*webEntry{"web": entry}, emit: func(e Event) { events = append(events, e) }}
				result := action.run(b)
				if result.OK || result.Message != message || len(events) != 1 {
					t.Fatalf("result=%+v events=%+v", result, events)
				}
				if e := events[0]; e.Service != "web" || e.Action != action.name || e.Kind != eventKindError || e.Message != message {
					t.Fatalf("event=%+v", e)
				}
				events = nil
				cascade := b.operationResult(t.Context(), "web", string(rules.ActionStart))
				if cascade.Status != operation.ResultFailed || cascade.Message != message || len(events) != 0 {
					t.Fatalf("cascade result=%+v events=%+v", cascade, events)
				}
			})
		}
	}
}

func (m fakeManager) Status(context.Context, string) (servicemgr.ServiceStatus, error) {
	if m.stopped != nil && *m.stopped {
		return servicemgr.ServiceStatus{Status: servicemgr.StatusInactive}, nil
	}
	return servicemgr.ServiceStatus{Status: servicemgr.StatusActive}, nil
}
func (m fakeManager) Start(context.Context, string) error {
	if m.stopped != nil {
		*m.stopped = false
	}
	return nil
}
func (m fakeManager) Stop(context.Context, string) error {
	if m.stopped != nil {
		*m.stopped = true
	}
	return nil
}
func (fakeManager) Restart(context.Context, string) error {
	return nil
}
func (fakeManager) Reload(context.Context, string) error                 { return nil }
func (fakeManager) SupportsReload(context.Context, string) (bool, error) { return true, nil }
func (fakeManager) ResetState(context.Context, string) error             { return nil }

func TestWebBackendOperateEmitsEvent(t *testing.T) {
	var events []Event
	dir := t.TempDir()
	locker := locks.NewOperationLocker(locks.RuntimeOpsDir(dir))
	engine := operation.New(operation.Config{
		Service: "web",
		Unit:    "nginx",
		Backend: string(servicemgr.BackendSystemd),
		Tree:    map[string]any{"policy": map[string]any{"cooldown": "5m"}},
		Manager: fakeManager{},
		Locker:  &locker,
		Scanner: locks.NewScanner(locks.RuntimeLocksDir(dir)),
		CheckDeps: checks.Deps{
			DefaultTimeout: time.Second,
			Status: func(context.Context) (servicemgr.Status, error) {
				return servicemgr.StatusActive, nil
			},
		},
		Emit: operationEventEmitter(func(e Event) { events = append(events, e) }),
	})

	b := &WebBackend{
		entries: map[string]*webEntry{
			"web": {engine: engine},
		},
		emit: func(e Event) { events = append(events, e) },
	}

	res := b.Operate(context.Background(), "web", string(rules.ActionStart), web.OperateOpts{})
	if !res.OK {
		t.Fatalf("operate: %+v", res)
	}
	if len(events) != 1 {
		t.Fatalf("want one action event, got %+v", events)
	}
	if events[0].Kind != eventKindAction || events[0].Action != string(rules.ActionStart) || events[0].Service != "web" {
		t.Fatalf("event = %+v", events[0])
	}

	b.Operate(context.Background(), "missing", string(rules.ActionStop), web.OperateOpts{})
	if len(events) != 2 || events[1].Kind != eventKindError {
		t.Fatalf("unknown service should emit error: %+v", events[1:])
	}
}

func TestWebBackendOperateStopStartSyncsMonitoring(t *testing.T) {
	stopped := false
	var events []Event
	store := newFakeStore()
	store.active["web"] = true
	store.source["web"] = state.SourceConfig

	dir := t.TempDir()
	locker := locks.NewOperationLocker(locks.RuntimeOpsDir(dir))
	engine := operation.New(operation.Config{
		Service: "web",
		Unit:    "nginx",
		Backend: string(servicemgr.BackendSystemd),
		Tree:    map[string]any{"policy": map[string]any{"cooldown": "5m"}},
		Manager: fakeManager{stopped: &stopped},
		Locker:  &locker,
		Scanner: locks.NewScanner(locks.RuntimeLocksDir(dir)),
		CheckDeps: checks.Deps{
			DefaultTimeout: time.Second,
			Status: func(context.Context) (servicemgr.Status, error) {
				return servicemgr.StatusActive, nil
			},
		},
	})

	b := &WebBackend{
		entries: map[string]*webEntry{
			"web": {engine: engine},
		},
		store:             store,
		operationSettling: store,
		emit:              func(e Event) { events = append(events, e) },
	}

	res := b.Operate(context.Background(), "web", "stop", web.OperateOpts{})
	if !res.OK {
		t.Fatalf("stop: %+v", res)
	}
	if store.active["web"] || store.source["web"] != state.SourceWebManualStop {
		t.Fatalf("store after stop active=%v source=%q", store.active["web"], store.source["web"])
	}
	if _, found, _ := store.OperationSettling("web"); found {
		t.Fatal("stop should clear operation settling after pausing monitoring")
	}
	if len(events) != 1 || events[0].Action != eventActionUnmonitor || events[0].Message != "monitoring paused after manual stop" {
		t.Fatalf("stop events = %+v", events)
	}

	res = b.Operate(context.Background(), "web", string(rules.ActionStart), web.OperateOpts{})
	if !res.OK {
		t.Fatalf("start: %+v", res)
	}
	if !store.active["web"] || store.source["web"] != state.SourceWeb {
		t.Fatalf("store after start active=%v source=%q", store.active["web"], store.source["web"])
	}
	rec, found, err := store.OperationSettling("web")
	if err != nil || !found {
		t.Fatalf("start should leave post-operation settling: found=%v err=%v", found, err)
	}
	if rec.Phase != state.OperationSettlingRelaunched {
		t.Fatalf("start settling = %+v", rec)
	}
	if len(events) != 2 || events[1].Action != eventActionMonitor || events[1].Message != "monitoring resumed after manual start" {
		t.Fatalf("start events = %+v", events)
	}
}

// Ensure fakeManager satisfies the interface at compile time.
var _ servicemgr.Manager = fakeManager{}

func TestWebBackendMetricsRejectsUnknownCheck(t *testing.T) {
	b := &WebBackend{
		entries: map[string]*webEntry{
			"web": {
				checkNames: []string{"http", "cmd"},
				checkTypes: map[string]string{"http": "http", "cmd": "command"},
			},
		},
	}
	if _, ok := b.Metrics(context.Background(), "web", "ghost", "", time.Hour); ok {
		t.Fatal("unknown check name must not be accepted")
	}
	if _, ok := b.Metrics(context.Background(), "web", "cmd", "", time.Hour); ok {
		t.Fatal("non-measured check type must not be accepted")
	}
	if _, ok := b.Metrics(context.Background(), "missing", "http", "", time.Hour); ok {
		t.Fatal("unknown service must not be accepted")
	}
	if _, ok := b.Metrics(context.Background(), "web", "http", "", time.Hour); !ok {
		t.Fatal("configured measured check must be accepted")
	}
}
