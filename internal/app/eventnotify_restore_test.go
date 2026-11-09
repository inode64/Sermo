package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/notify"
	"sermo/internal/rules"
	"sermo/internal/severity"
	"sermo/internal/state"
)

func openEventNotifyStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.OpenContextWith(t.Context(), filepath.Join(t.TempDir(), state.Filename), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newRestoreRouter(store *state.Store, now *time.Time, targets ...notify.Notifier) *EventNotifier {
	router := NewEventNotifier("host-a", slog.New(slog.NewTextHandler(io.Discard, nil)), nil, store)
	router.now = func() time.Time { return *now }
	configured := map[string]notify.Notifier{}
	names := make([]string, 0, len(targets))
	for _, target := range targets {
		configured[target.Name()] = target
		names = append(names, target.Name())
	}
	router.Update(config.EventNotification{Targets: names, RepeatInterval: 24 * time.Hour}, configured)
	return router
}

// Only open health incidents of a service check or rule are handed to the
// workers, at the gravest grade a configured notifier holds them.
func TestOpenServiceIncidentsListsWhatConfiguredNotifiersHold(t *testing.T) {
	store := openEventNotifyStore(t)
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	ops, old := &fakeNotifier{name: "ops"}, &fakeNotifier{name: "old"}

	removed := newRestoreRouter(store, &now, old)
	removed.deliver(t.Context(), Event{Service: "web", Check: "strays", Kind: eventKindFiring})

	router := newRestoreRouter(store, &now, ops)
	for _, e := range []Event{
		{Service: "web", Check: "http", Kind: eventKindFiring, Severity: severity.Warning},
		{Service: "web", Check: "http", Kind: eventKindFiring, Severity: severity.Critical},
		{Service: "web", Check: "port", Kind: eventKindFiring},
		{Service: "web", Check: "port", Kind: eventKindRecovered},
		{Service: "web", Rule: "ready", Action: "restart", Kind: eventKindDryRun, Severity: severity.Error},
		{Service: "web", Rule: "repair", Action: "restart", Kind: eventKindError, Message: "preflight failed"},
		{Service: "db", Rule: "warn-binlog", Kind: eventKindAlert, Severity: severity.Warning},
		{Watch: "disk", Kind: eventKindFiring},
		{App: "salt-minion", Kind: eventKindFiring},
	} {
		router.deliver(t.Context(), e)
	}

	got := router.openServiceIncidents()
	want := map[string]serviceIncidents{
		"web": {checks: map[string]severity.Level{"http": severity.Critical}, rules: map[string]severity.Level{"ready": severity.Error}},
		"db":  {rules: map[string]severity.Level{"warn-binlog": severity.Warning}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("open service incidents = %+v, want %+v (no closed episode, operational error, watch, app or removed notifier)", got, want)
	}
}

// A check whose snapshot did not survive the restart (another configuration)
// adopts the open incident: the first healthy observation sends its recovery
// at the incident's grade, and a failing one continues it silently.
func TestRestoredIncidentCheckRecoversOnceAndNeverReannounces(t *testing.T) {
	healthy := checks.Result{Check: "strays", OK: true, Message: "no stray processes"}
	failing := checks.Result{Check: "strays", Message: "1 stray process"}
	busy := checks.Result{Check: "strays", Busy: true, Message: "server busy: too many connections"}
	// A long window keeps the restart from running in these few cycles.
	restartReading := []rules.Rule{{
		Name: "restart-strays", Type: rules.RuleRemediation,
		If:      map[string]any{rules.ConditionFailed: map[string]any{rules.FieldCheck: "strays"}},
		For:     &rules.ForWindow{Cycles: 10},
		Actions: []rules.Action{{Type: rules.ActionRestart}},
	}}
	tests := []struct {
		name     string
		rules    []rules.Rule
		names    map[string]bool
		cycles   []checks.Result
		wantKind string
	}{
		{name: "healthy at once", names: map[string]bool{"strays": true}, cycles: []checks.Result{healthy}, wantKind: "recovered/critical"},
		{name: "failing then healthy", names: map[string]bool{"strays": true}, cycles: []checks.Result{failing, failing, healthy}, wantKind: "recovered/critical"},
		{name: "still failing", names: map[string]bool{"strays": true}, cycles: []checks.Result{failing, failing}},
		{name: "check no longer configured", names: map[string]bool{"other": true}, cycles: []checks.Result{healthy}},
		{
			name:  "check a rule now reads, healthy",
			names: map[string]bool{"strays": true},
			rules: []rules.Rule{{
				Name: "warn-strays", Type: rules.RuleAlert,
				If:      map[string]any{rules.ConditionFailed: map[string]any{rules.FieldCheck: "strays"}},
				Actions: []rules.Action{{Type: rules.ActionAlert, Message: "strays"}},
			}},
			cycles:   []checks.Result{healthy},
			wantKind: "recovered/critical",
		},
		{
			// The rule announces the failure; the check's old incident stays quiet.
			name:  "check a rule now reads, failing",
			names: map[string]bool{"strays": true},
			rules: []rules.Rule{{
				Name: "warn-strays", Type: rules.RuleAlert,
				If:      map[string]any{rules.ConditionFailed: map[string]any{rules.FieldCheck: "strays"}},
				Actions: []rules.Action{{Type: rules.ActionAlert, Message: "strays"}},
			}},
			cycles: []checks.Result{failing, failing},
		},
		{
			// Only a restart reads the check, which makes it a busy-reported
			// check: its restored incident must still close.
			name:     "check an operation rule now reads, healthy",
			names:    map[string]bool{"strays": true},
			rules:    restartReading,
			cycles:   []checks.Result{healthy},
			wantKind: "recovered/critical",
		},
		{
			name:   "check an operation rule now reads, failing",
			names:  map[string]bool{"strays": true},
			rules:  restartReading,
			cycles: []checks.Result{failing, failing},
		},
		{
			// A busy server continues the open incident without a second
			// announcement, and its recovery closes it once.
			name:     "check an operation rule now reads, busy then healthy",
			names:    map[string]bool{"strays": true},
			rules:    restartReading,
			cycles:   []checks.Result{busy, healthy},
			wantKind: "recovered/critical",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var events []Event
			cycle := 0
			w := &Worker{
				Service:    "web",
				Rules:      tt.rules,
				DryRun:     true,
				checkNames: tt.names,
				Checks: func(context.Context, checks.Deps) map[string]checks.Result {
					result := tt.cycles[cycle]
					cycle++
					return map[string]checks.Result{"strays": result}
				},
				Emit: func(e Event) { events = append(events, e) },
			}
			w.restoreIncidents(serviceIncidents{checks: map[string]severity.Level{"strays": severity.Critical}})
			for range tt.cycles {
				w.RunCycle(context.Background())
			}
			var got []string
			for _, e := range events {
				if e.Check == "strays" && (e.Kind == eventKindFiring || e.Kind == eventKindRecovered) {
					got = append(got, e.Kind+"/"+e.Severity.String())
				}
			}
			if strings.Join(got, ",") != tt.wantKind {
				t.Fatalf("health events = %v, want %q", got, tt.wantKind)
			}
		})
	}
}

// A rule whose window did not survive the restart recovers its open incident
// the first time its condition is observed not to hold; an unknown cycle
// proves nothing, and a firing rule carries the incident on its own episode.
func TestRestoredIncidentRuleRecoversOnlyOnAnObservedClearCondition(t *testing.T) {
	ok := checks.Result{Check: "ready", OK: true, Message: "status 200"}
	down := checks.Result{Check: "ready", Message: "connection refused"}
	unknown := checks.Result{Check: "ready", Skipped: true, Message: "gated off"}
	tests := []struct {
		name       string
		ruleType   rules.RuleType
		firing     bool
		cycles     []checks.Result
		wantEvents string
	}{
		{name: "alert rule clear", ruleType: rules.RuleAlert, cycles: []checks.Result{ok, ok}, wantEvents: "recovered/error"},
		{name: "remediation rule clear", ruleType: rules.RuleRemediation, cycles: []checks.Result{ok, ok}, wantEvents: "recovered/error"},
		{name: "unknown then clear", ruleType: rules.RuleAlert, cycles: []checks.Result{unknown, ok}, wantEvents: "recovered/error"},
		{name: "still failing", ruleType: rules.RuleAlert, cycles: []checks.Result{down, ok}, wantEvents: "alert,recovered/error"},
		{name: "restored window recovers itself", ruleType: rules.RuleAlert, firing: true, cycles: []checks.Result{ok, ok}, wantEvents: "recovered/warning"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action := rules.Action{Type: rules.ActionAlert, Message: "ready is down"}
			if tt.ruleType == rules.RuleRemediation {
				action = rules.Action{Type: rules.ActionRestart}
			}
			rule := rules.Rule{
				Name: "ready", Type: tt.ruleType,
				If:      map[string]any{rules.ConditionFailed: map[string]any{rules.FieldCheck: "ready"}},
				Actions: []rules.Action{action},
			}
			var events []Event
			cycle := 0
			w := &Worker{
				Service: "alloy",
				Rules:   []rules.Rule{rule},
				DryRun:  true,
				Checks: func(context.Context, checks.Deps) map[string]checks.Result {
					result := tt.cycles[cycle]
					cycle++
					return map[string]checks.Result{"ready": result}
				},
				Emit: func(e Event) { events = append(events, e) },
			}
			if tt.firing {
				window := &rules.WindowState{}
				window.FiresGradedAt(rule, true, severity.Warning, time.Now())
				w.windows = map[string]*rules.WindowState{"ready": window}
			}
			w.restoreIncidents(serviceIncidents{rules: map[string]severity.Level{"ready": severity.Error}})
			for range tt.cycles {
				w.RunCycle(context.Background())
			}
			var got []string
			for _, e := range events {
				switch {
				case e.Rule != "ready":
				case e.Kind == eventKindRecovered:
					got = append(got, e.Kind+"/"+e.Severity.String())
				case e.Kind == eventKindAlert:
					got = append(got, e.Kind)
				}
			}
			if strings.Join(got, ",") != tt.wantEvents {
				t.Fatalf("rule events = %v, want %q", got, tt.wantEvents)
			}
		})
	}
}

// End to end across a restart: incidents opened by the old process close with
// one recovery each once the new workers observe the service healthy, and are
// no longer reminded.
func TestRestartClosesIncidentsTheEpisodeMemoryLost(t *testing.T) {
	store := openEventNotifyStore(t)
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	ops := &fakeNotifier{name: "ops"}
	before := newRestoreRouter(store, &now, ops)
	before.deliver(t.Context(), Event{Service: "alloy", Check: "strays", Kind: eventKindFiring, Message: "check strays: 1 stray process"})
	before.deliver(t.Context(), Event{Service: "alloy", Rule: "ready", Action: "restart", Kind: eventKindDryRun, Message: "would restart"})
	if len(ops.msgs) != 2 {
		t.Fatalf("setup delivered %d messages, want 2", len(ops.msgs))
	}

	router := newRestoreRouter(store, &now, ops)
	w := &Worker{
		Service:    "alloy",
		checkNames: map[string]bool{"strays": true, "ready": true},
		Rules: []rules.Rule{{
			Name: "ready", Type: rules.RuleRemediation,
			If:      map[string]any{rules.ConditionFailed: map[string]any{rules.FieldCheck: "ready"}},
			Actions: []rules.Action{{Type: rules.ActionRestart}},
		}},
		DryRun: true,
		Checks: func(context.Context, checks.Deps) map[string]checks.Result {
			return map[string]checks.Result{
				"strays": {Check: "strays", OK: true, Message: "no stray processes"},
				"ready":  {Check: "ready", OK: true, Message: "status 200"},
			}
		},
		Emit: func(e Event) {
			if eventNeedsNotification(e) {
				router.deliver(t.Context(), e)
			}
		},
	}
	restoreServiceIncidents([]*Worker{w}, router.openServiceIncidents())
	w.RunCycle(context.Background())
	w.RunCycle(context.Background())

	recovered := ops.msgs[2:]
	if len(recovered) != 2 {
		t.Fatalf("after restart sent %q, want one recovery per open incident", subjects(recovered))
	}
	for _, msg := range recovered {
		if msg.Fields[sermoEnvEvent] != eventKindRecovered {
			t.Fatalf("message %q is not a recovery", msg.Subject)
		}
	}
	if open := router.openServiceIncidents(); len(open) != 0 {
		t.Fatalf("incidents still open after recovery: %+v", open)
	}
	now = now.Add(48 * time.Hour)
	router.remind(t.Context())
	if len(ops.msgs) != 4 {
		t.Fatalf("closed incidents were reminded: %q", subjects(ops.msgs[4:]))
	}
}

// A state sensor opens an episode only when it cannot run (a starved check
// slot); its next verdictless sample is the recovery, also for an episode
// restored from an open incident after a restart.
func TestVerdictlessCheckRecoversWhenItRunsAgain(t *testing.T) {
	starved := checks.Result{Check: "strays", Reports: checks.ReportsState, Unavailable: true,
		Message: "check not started: wait for check slot: context deadline exceeded"}
	sensed := checks.Result{Check: "strays", Reports: checks.ReportsState, OK: true, Message: "no stray processes"}
	tests := []struct {
		name     string
		restored bool
		cycles   []checks.Result
		want     string
	}{
		{name: "starved then sensed", cycles: []checks.Result{starved, sensed, sensed}, want: "firing,recovered"},
		{name: "restored incident", restored: true, cycles: []checks.Result{sensed, sensed}, want: "recovered"},
		{name: "never starved", cycles: []checks.Result{sensed, sensed}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var events []Event
			cycle := 0
			w := &Worker{
				Service:    "web",
				checkNames: map[string]bool{"strays": true},
				Checks: func(context.Context, checks.Deps) map[string]checks.Result {
					result := tt.cycles[cycle]
					cycle++
					return map[string]checks.Result{"strays": result}
				},
				Emit: func(e Event) { events = append(events, e) },
			}
			if tt.restored {
				w.restoreIncidents(serviceIncidents{checks: map[string]severity.Level{"strays": severity.Error}})
			}
			for range tt.cycles {
				w.RunCycle(context.Background())
			}
			var got []string
			for _, e := range events {
				if e.Check == "strays" && (e.Kind == eventKindFiring || e.Kind == eventKindRecovered) {
					got = append(got, e.Kind)
				}
			}
			if strings.Join(got, ",") != tt.want {
				t.Fatalf("health events = %v, want %q", got, tt.want)
			}
		})
	}
}
