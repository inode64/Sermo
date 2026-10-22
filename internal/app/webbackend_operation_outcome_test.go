package app

import (
	"context"
	"slices"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/execx/execxtest"
	"sermo/internal/metrics"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
	"sermo/internal/web"
)

// A blocked restart stays on the service after a check fires: last_event moves
// on, last_operation keeps the outcome until a newer operation replaces it.
func TestServiceViewKeepsTheLastOperationOutcome(t *testing.T) {
	log := NewEventLog(20)
	restart := string(rules.ActionRestart)
	log.Add(eventFromOperationResult(operation.Result{Service: "alloy", Action: restart, Status: operation.ResultBlocked, Message: "active service has no process matching configured exact exe/user selectors"}))
	log.Add(Event{Service: "alloy", Kind: eventKindFiring, Message: "check service: status inactive"})
	log.Add(Event{Service: "alloy", Kind: eventKindDryRun, Rule: "ready", Action: restart, Message: "would restart"})
	b := &WebBackend{order: []string{"alloy"}, entries: map[string]*webEntry{"alloy": {}}, events: log}

	svc := b.view(context.Background(), "alloy", b.entries["alloy"])
	if svc.LastEvent == nil || svc.LastEvent.Kind != eventKindDryRun {
		t.Fatalf("last_event = %+v, want the newest event", svc.LastEvent)
	}
	if svc.LastOperation == nil || svc.LastOperation.Action != restart || svc.LastOperation.Status != eventStatusBlocked {
		t.Fatalf("last_operation = %+v, want the blocked restart", svc.LastOperation)
	}

	log.Add(eventFromOperationResult(operation.Result{Service: "alloy", Action: restart, Status: operation.ResultOK, Message: "restart ok"}))
	if svc := b.view(context.Background(), "alloy", b.entries["alloy"]); svc.LastOperation == nil || svc.LastOperation.Status != eventStatusOK {
		t.Fatalf("last_operation = %+v, want the newer successful restart", svc.LastOperation)
	}
}

// Every lifecycle outcome counts, from any source; events without a result
// status (dry runs, cooldown suppressions, rejected requests) never do.
func TestLatestOperationOutcomesSelectsResultEvents(t *testing.T) {
	restart, stop, start := string(rules.ActionRestart), string(rules.ActionStop), string(rules.ActionStart)
	events := []Event{
		eventFromOperationResult(operation.Result{Service: "web", Action: stop, Status: operation.ResultOrphanProcesses}),
		{Service: "web", Kind: eventKindDryRun, Rule: "ready", Action: restart, Message: "would restart"},
		{Service: "web", Kind: eventKindSuppressed, Rule: "ready", Action: restart, Message: "cooldown"},
		{Service: "web", Kind: eventKindError, Action: restart, Message: "service web is disabled in configuration"},
		{Service: "web", Kind: eventKindAction, Action: eventActionReleaseLock, Status: eventStatusOK},
		{Service: "db", Kind: eventKindError, Rule: "ready", Action: restart, Status: string(operation.ResultFailed)},
		{Service: "cache", Kind: eventKindCascade, Action: start, Status: string(operation.ResultOK), Message: "cascade from web"},
		{Watch: "storage-root", Kind: eventKindError, Action: restart, Status: string(operation.ResultFailed)},
	}
	check := func(t *testing.T, got map[string]LoggedEvent) {
		t.Helper()
		if len(got) != 3 || got["web"].Action != stop || got["web"].Status != string(operation.ResultOrphanProcesses) ||
			got["db"].Status != string(operation.ResultFailed) || got["cache"].Kind != eventKindCascade {
			t.Fatalf("outcomes = %+v", got)
		}
	}
	t.Run("ring", func(t *testing.T) {
		log := NewEventLog(20)
		for _, e := range events {
			log.Add(e)
		}
		check(t, log.LatestOperationOutcomes("", serviceLifecycleActions))
		if one := log.LatestOperationOutcomes("db", serviceLifecycleActions); len(one) != 1 || one["db"].Status != string(operation.ResultFailed) {
			t.Fatalf("one service = %+v", one)
		}
	})
	t.Run("store", func(t *testing.T) {
		store := &stubEventStore{}
		log, err := NewPersistentEventLog(20, store, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			log.Add(e)
		}
		// sermoctl records its operation straight to the store.
		store.rows = append(store.rows, state.EventRecord{ID: 99, Service: "api", Kind: eventKindSuppressed, Action: restart, Status: eventStatusBlocked})
		got := log.LatestOperationOutcomes("", serviceLifecycleActions)
		if got["api"].Status != eventStatusBlocked {
			t.Fatalf("outcome written by another process = %+v", got["api"])
		}
		delete(got, "api")
		check(t, got)
	})
	t.Run("store failure falls back to the ring", func(t *testing.T) {
		store := &stubEventStore{failFor: "outcomes"}
		var storeErr error
		log, err := NewPersistentEventLog(20, store, func(err error) { storeErr = err })
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			log.Add(e)
		}
		check(t, log.LatestOperationOutcomes("", serviceLifecycleActions))
		if storeErr == nil {
			t.Fatal("store failure was not reported")
		}
	})
}

func TestServiceViewForecastsAnIdentityRefusal(t *testing.T) {
	t0 := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	unverified := process.PrincipalIdentity{Attributed: true, Required: true}
	tests := []struct {
		name      string
		mode      config.ServiceProcessMode
		status    servicemgr.Status
		principal process.PrincipalIdentity
		age       time.Duration
		want      bool
	}{
		{name: "active daemon of another user", mode: config.ServiceProcessResident, status: servicemgr.StatusActive, principal: unverified, age: 5 * time.Second, want: true},
		{name: "trusted daemon", mode: config.ServiceProcessResident, status: servicemgr.StatusActive, principal: process.PrincipalIdentity{Attributed: true, Required: true, Trusted: true}, age: 5 * time.Second},
		{name: "stale sample", mode: config.ServiceProcessResident, status: servicemgr.StatusActive, principal: unverified, age: time.Hour},
		{name: "inactive service", mode: config.ServiceProcessResident, status: servicemgr.StatusInactive, principal: unverified, age: 5 * time.Second},
		{name: "process-free service", mode: config.ServiceProcessNone, status: servicemgr.StatusActive, principal: unverified, age: 5 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metrics := NewServiceMetricSampler(nil)
			metrics.record(t.Context(), "alloy", web.ServiceRuntime{Count: 1}, tt.principal, t0)
			now := t0.Add(tt.age)
			b := &WebBackend{
				order: []string{"alloy"},
				entries: map[string]*webEntry{"alloy": {
					interval: 30 * time.Second,
					status:   func(context.Context) (servicemgr.Status, error) { return tt.status, nil },
					engine:   operation.Engine{Lifecycle: config.ServiceLifecycle{ProcessMode: tt.mode}},
				}},
				serviceMetrics: metrics,
				now:            func() time.Time { return now },
			}
			e := b.entries["alloy"]
			svc := b.viewWithRuntime(context.Background(), "alloy", e, b.observeService("alloy", e), nil, serviceLockView{ready: true})
			if (len(svc.OperationForecast) > 0) != tt.want {
				t.Fatalf("forecast = %+v, want %v", svc.OperationForecast, tt.want)
			}
			if !tt.want {
				return
			}
			i := slices.IndexFunc(svc.OperationForecast, func(f web.OperationForecast) bool { return f.Action == string(rules.ActionRestart) })
			if i < 0 || svc.OperationForecast[i].Status != eventStatusBlocked || svc.OperationForecast[i].Message == "" {
				t.Fatalf("forecast = %+v, want a blocked restart with the engine message", svc.OperationForecast)
			}
		})
	}
}

type walkCountingReader struct {
	serviceOwnershipReader
	walks *int
}

func (r walkCountingReader) PIDs() ([]int, error) {
	*r.walks++
	return r.serviceOwnershipReader.PIDs()
}

// The worker publishes the identity evidence of the discovery its cycle already
// ran, so the forecast costs no second walk of the process table.
func TestWorkerPublishesPrincipalIdentityFromTheCycleDiscovery(t *testing.T) {
	for _, tt := range []struct {
		user       string
		unverified bool
	}{
		{user: "500", unverified: true},
		{user: "0"},
	} {
		t.Run("selector user "+tt.user, func(t *testing.T) {
			walks := 0
			ids := serviceOwnershipReader{100: {PID: 100, PPID: 1, UID: 0, Exe: "/opt/app/main", ExeOK: true, StartTicks: 10, StartTicksOK: true}}
			collector := metrics.New(&liveCPUReader{cpu: map[int]uint64{100: 0}, hz: 100, ncpu: 2})
			sampler := NewServiceMetricSampler(nil)
			tree := map[string]any{"processes": map[string]any{
				"main": map[string]any{"type": "command_match", "exe": "/opt/app/main", "user": tt.user},
			}}
			w, _, warnings := buildWorker(t.Context(), "svc", "svc.service", tree, Deps{
				Backend: servicemgr.BackendSystemd, Manager: fakeManager{}, Runtime: t.TempDir(),
				DefaultTimeout: time.Second, OperationTimeout: time.Second,
				ProcReader:  process.NewCachingReader(walkCountingReader{serviceOwnershipReader: ids, walks: &walks}, time.Hour),
				BackendPIDs: func() []int { return []int{100} }, ExecxRunner: &execxtest.Runner{},
				Live: NewLiveMetrics(), LiveCollector: collector, ServiceMetrics: sampler,
				Emit: func(Event) {},
			}, nil)
			if len(warnings) != 0 {
				t.Fatalf("warnings: %v", warnings)
			}
			w.RunCycle(t.Context())
			_, principal, ok := sampler.latest("svc")
			if !ok || !principal.Attributed || principal.Unverified() != tt.unverified {
				t.Fatalf("published principal = %+v (ok=%v), want unverified=%v", principal, ok, tt.unverified)
			}
			if walks != 1 {
				t.Fatalf("process table walks = %d, want the cycle's single walk", walks)
			}
		})
	}
}
