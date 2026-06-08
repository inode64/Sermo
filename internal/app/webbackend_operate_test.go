package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/locks"
	"sermo/internal/operation"
	"sermo/internal/servicemgr"
)

type fakeManager struct{}

func (fakeManager) Status(context.Context, string) (servicemgr.ServiceStatus, error) {
	return servicemgr.ServiceStatus{Status: servicemgr.StatusActive}, nil
}
func (fakeManager) Start(context.Context, string) error { return nil }
func (fakeManager) Stop(context.Context, string) error  { return nil }
func (fakeManager) Restart(context.Context, string) error {
	return nil
}

func TestWebBackendOperateEmitsEvent(t *testing.T) {
	var events []Event
	dir := t.TempDir()
	locker := locks.NewOperationLocker(filepath.Join(dir, "ops"))
	engine := operation.New(operation.Config{
		Service: "web",
		Unit:    "nginx",
		Backend: "systemd",
		Tree:    map[string]any{"policy": map[string]any{"cooldown": "5m"}},
		Manager: fakeManager{},
		Locker:  &locker,
		Scanner: locks.NewScanner(filepath.Join(dir, "locks")),
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

	res := b.Operate(context.Background(), "web", "start")
	if !res.OK {
		t.Fatalf("operate: %+v", res)
	}
	if len(events) != 1 {
		t.Fatalf("want one action event, got %+v", events)
	}
	if events[0].Kind != "action" || events[0].Action != "start" || events[0].Service != "web" {
		t.Fatalf("event = %+v", events[0])
	}

	b.Operate(context.Background(), "missing", "stop")
	if len(events) != 2 || events[1].Kind != "error" {
		t.Fatalf("unknown service should emit error: %+v", events[1:])
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
	if _, ok := b.Metrics(context.Background(), "web", "ghost", time.Hour); ok {
		t.Fatal("unknown check name must not be accepted")
	}
	if _, ok := b.Metrics(context.Background(), "web", "cmd", time.Hour); ok {
		t.Fatal("non-measured check type must not be accepted")
	}
	if _, ok := b.Metrics(context.Background(), "missing", "http", time.Hour); ok {
		t.Fatal("unknown service must not be accepted")
	}
	if _, ok := b.Metrics(context.Background(), "web", "http", time.Hour); !ok {
		t.Fatal("configured measured check must be accepted")
	}
}
