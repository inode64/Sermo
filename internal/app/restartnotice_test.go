package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/notify"
	"sermo/internal/process"
	"sermo/internal/rules"
	"sermo/internal/state"
)

func TestWorkerServiceRestartNoticeIsOneShotAcrossWorkers(t *testing.T) {
	store := newFakeStore()
	n := &fakeNotifier{name: "ops"}
	principal := servicePrimaryProcess{
		process:   process.Process{PID: 42, Exe: "/usr/sbin/snmpd", Role: process.RoleMain, Source: process.SourceBackend},
		startedAt: t0.Add(-2 * time.Minute),
	}
	notice := &config.ServiceRestartNotice{
		UptimeBelow: 5 * time.Minute,
		Notify:      []string{"ops"},
		Subject:     "${restart.service} (${restart.unit}) restarted",
		Message:     "${restart.process} pid ${restart.pid} up ${restart.uptime} since ${restart.started_at}; limit ${restart.threshold}",
	}
	h := &workerHarness{}
	w := h.worker(nil, rules.Policy{}, nil)
	w.Unit = "snmpd.service"
	w.RestartNotice = notice
	w.ServiceRestartNotice = store
	w.PrimaryProcess = func() (servicePrimaryProcess, bool) { return principal, true }
	w.Notifiers = map[string]notify.Notifier{"ops": n}

	w.RunCycle(context.Background())
	w.RunCycle(context.Background())

	if got := h.countEvents(eventKindAlert); got != 1 {
		t.Fatalf("alert events = %d, want one: %+v", got, h.events)
	}
	if got := len(n.msgs); got != 1 {
		t.Fatalf("notifications = %d, want one", got)
	}
	message := n.msgs[0]
	if message.Subject != "web (snmpd.service) restarted" {
		t.Fatalf("subject = %q", message.Subject)
	}
	if message.Body != "/usr/sbin/snmpd pid 42 up 120s since 2026-06-06T11:58:00Z; limit 5m0s" {
		t.Fatalf("body = %q", message.Body)
	}
	for key, want := range map[string]string{
		sermoEnvEvent:              restartNoticeRule,
		sermoEnvRestartService:     "web",
		sermoEnvRestartUnit:        "snmpd.service",
		sermoEnvRestartProcess:     "/usr/sbin/snmpd",
		sermoEnvRestartPID:         "42",
		sermoEnvRestartUptime:      "120s",
		sermoEnvRestartUptimeSecs:  "120",
		sermoEnvRestartStartedAt:   "2026-06-06T11:58:00Z",
		sermoEnvRestartUptimeBelow: "5m0s",
	} {
		if got := message.Fields[key]; got != want {
			t.Errorf("field %s = %q, want %q", key, got, want)
		}
	}

	// A rebuilt worker represents a sermod restart/config reload. The persisted
	// identity must keep the same young process from delivering a second notice.
	afterRestart := &workerHarness{}
	w2 := afterRestart.worker(nil, rules.Policy{}, nil)
	w2.Unit = w.Unit
	w2.RestartNotice = notice
	w2.ServiceRestartNotice = store
	w2.PrimaryProcess = w.PrimaryProcess
	w2.Notifiers = w.Notifiers
	w2.RunCycle(context.Background())
	if got := len(n.msgs); got != 1 {
		t.Fatalf("notifications after worker rebuild = %d, want one", got)
	}
}

func TestWorkerServiceRestartNoticeSuppressesSermoOperation(t *testing.T) {
	store := newFakeStore()
	store.now = func() time.Time { return t0 }
	if err := store.SetOperationSettling("web", "restart", state.OperationSettlingSettling, state.SourceWeb); err != nil {
		t.Fatalf("SetOperationSettling: %v", err)
	}
	n := &fakeNotifier{name: "ops"}
	h := &workerHarness{}
	w := h.worker(nil, rules.Policy{}, nil)
	w.RestartNotice = restartNoticeConfig()
	w.ServiceRestartNotice = store
	w.OperationSettling = store
	w.PrimaryProcess = recentPrimaryProcess
	w.Notifiers = map[string]notify.Notifier{"ops": n}

	w.RunCycle(context.Background())
	w.RunCycle(context.Background())

	if got := len(n.msgs); got != 0 {
		t.Fatalf("Sermo operation produced %d external-restart notifications", got)
	}
	if got := h.countEvents(eventKindAlert); got != 0 {
		t.Fatalf("Sermo operation produced alert events: %+v", h.events)
	}
}

func TestWorkerServiceRestartNoticeSamplesBeforeSlowChecks(t *testing.T) {
	store := newFakeStore()
	n := &fakeNotifier{name: "ops"}
	at := t0
	h := &workerHarness{}
	w := h.worker(nil, rules.Policy{}, nil)
	w.Now = func() time.Time { return at }
	w.RestartNotice = restartNoticeConfig()
	w.ServiceRestartNotice = store
	w.PrimaryProcess = func() (servicePrimaryProcess, bool) {
		return servicePrimaryProcess{
			process:   process.Process{PID: 42, Role: process.RoleMain, Source: process.SourceBackend},
			startedAt: t0.Add(-2 * time.Minute),
		}, true
	}
	w.Notifiers = map[string]notify.Notifier{"ops": n}
	w.Checks = func(context.Context, checks.Deps) map[string]checks.Result {
		at = at.Add(4 * time.Minute) // a slow first cycle must not hide the restart
		return nil
	}

	w.RunCycle(context.Background())

	if got := h.countEvents(eventKindAlert); got != 1 {
		t.Fatalf("alert events = %d, want one: %+v", got, h.events)
	}
	if got := len(n.msgs); got != 1 {
		t.Fatalf("notifications = %d, want one", got)
	}
}

func TestWorkerServiceRestartNoticePanicAndStoreFailure(t *testing.T) {
	tests := []struct {
		name       string
		store      ServiceRestartNoticeStore
		panicMode  bool
		wantAlert  int
		wantNotify int
		wantKind   string
	}{
		{name: "panic", store: newFakeStore(), panicMode: true, wantAlert: 1, wantKind: eventKindNotifySuppressed},
		{name: "state failure", store: failingRestartNoticeStore{err: errors.New("disk full")}, wantKind: eventKindError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n := &fakeNotifier{name: "ops"}
			h := &workerHarness{}
			w := h.worker(nil, rules.Policy{}, nil)
			w.RestartNotice = restartNoticeConfig()
			w.ServiceRestartNotice = tc.store
			w.PrimaryProcess = recentPrimaryProcess
			w.Notifiers = map[string]notify.Notifier{"ops": n}
			w.InPanic = func() bool { return tc.panicMode }

			w.RunCycle(context.Background())
			if got := h.countEvents(eventKindAlert); got != tc.wantAlert {
				t.Fatalf("alert events = %d, want %d: %+v", got, tc.wantAlert, h.events)
			}
			if got := len(n.msgs); got != tc.wantNotify {
				t.Fatalf("notifications = %d, want %d", got, tc.wantNotify)
			}
			if _, ok := h.eventOf(tc.wantKind); !ok {
				t.Fatalf("missing %s event: %+v", tc.wantKind, h.events)
			}
		})
	}
}

func TestSelectPrimaryProcess(t *testing.T) {
	tests := []struct {
		name  string
		procs []process.Process
		want  int
		ok    bool
	}{
		{
			name: "backend main wins",
			procs: []process.Process{
				{PID: 2, Source: process.SourceBackend, Role: process.RoleMain},
				{PID: 3, Source: process.SelectorPidfile},
			},
			want: 2, ok: true,
		},
		{name: "pidfile", procs: []process.Process{{PID: 3, Role: process.SelectorPidfile, Source: process.SelectorPidfile}}, want: 3, ok: true},
		{name: "explicit main", procs: []process.Process{{PID: 4, Role: process.RoleMain, Source: process.SelectorCommandMatch}}, want: 4, ok: true},
		{name: "non-main pidfile is skipped", procs: []process.Process{{PID: 5, Role: "worker", Source: process.SelectorPidfile}}, ok: false},
		{name: "ambiguous roots are skipped", procs: []process.Process{{PID: 5, Source: process.SelectorCommandMatch}}, ok: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := selectPrimaryProcess(tc.procs)
			if ok != tc.ok || (ok && got.PID != tc.want) {
				t.Fatalf("selectPrimaryProcess(%+v) = %+v, %v; want pid %d, %v", tc.procs, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func restartNoticeConfig() *config.ServiceRestartNotice {
	return &config.ServiceRestartNotice{
		UptimeBelow: 5 * time.Minute,
		Notify:      []string{"ops"},
		Message:     "${restart.service} restarted",
	}
}

func recentPrimaryProcess() (servicePrimaryProcess, bool) {
	return servicePrimaryProcess{
		process:   process.Process{PID: 42, Exe: "/usr/sbin/service", Role: process.RoleMain, Source: process.SourceBackend},
		startedAt: t0.Add(-time.Minute),
	}, true
}

type failingRestartNoticeStore struct{ err error }

func (s failingRestartNoticeStore) ServiceRestartNotice(string) (state.ServiceRestartNoticeRecord, bool, error) {
	return state.ServiceRestartNoticeRecord{}, false, s.err
}

func (s failingRestartNoticeStore) SetServiceRestartNotice(string, state.ServiceRestartNoticeRecord) error {
	return s.err
}
