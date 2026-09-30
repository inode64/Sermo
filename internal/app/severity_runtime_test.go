package app

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/notify"
	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/severity"
	"sermo/internal/state"
)

// floorNotifier is a fake notifier with a min_severity, the way notify.Build
// wraps a configured one.
type floorNotifier struct {
	fakeNotifier
	floor severity.Level
}

func (f *floorNotifier) MinSeverity() severity.Level { return f.floor }

func graded(level severity.Level) checks.Result {
	return checks.Result{Check: "disk", OK: true, Condition: true, Message: "used " + level.String(), Severity: level}
}

func subjects(msgs []notify.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Subject)
	}
	return out
}

// A watch escalates only after a graver level held for its own window, holds
// the mark when the value eases, and closes at that mark. Each notifier hears
// the levels its min_severity admits, and the recovery reaches exactly the
// notifiers that heard the incident.
func TestWatchEscalateAndHoldRoutesBySeverity(t *testing.T) {
	healthy := checks.Result{Check: "disk", Condition: true, Message: "used 40%"}
	check := &scriptedCheck{results: []checks.Result{
		graded(severity.Warning), graded(severity.Warning), // opens at warning
		graded(severity.Critical), graded(severity.Critical), // sustained: escalates
		graded(severity.Warning), graded(severity.Warning), // eases off: held
		healthy, healthy, // recovers at critical
	}}
	all := &floorNotifier{name: "all", floor: severity.Debug}
	oncall := &floorNotifier{name: "oncall", floor: severity.Critical}
	errorsOnly := &floorNotifier{name: "errors", floor: severity.Error}
	var events []Event
	var hookLevels []string
	w := &Watch{
		Name: "disk-root", CheckType: checks.CheckTypeStorage, Check: check,
		Window:    rules.Rule{For: &rules.ForWindow{Cycles: 2}},
		Notifiers: []notify.Notifier{all, oncall, errorsOnly},
		Hook:      HookSpec{Command: []string{"/bin/true"}},
		Runner: HookRunnerFunc(func(_ context.Context, _ []string, env map[string]string, _ time.Duration) error {
			hookLevels = append(hookLevels, env[sermoEnvSeverity])
			return nil
		}),
		Emit: func(e Event) { events = append(events, e) },
	}
	for range 8 {
		w.RunCycle(context.Background())
	}

	var announced []string
	for _, e := range events {
		if e.Kind == eventKindFiring || e.Kind == eventKindRecovered {
			announced = append(announced, e.Kind+"/"+e.Severity.String())
		}
	}
	if got, want := strings.Join(announced, ","), "firing/warning,firing/critical,recovered/critical"; got != want {
		t.Fatalf("announcements = %s, want %s", got, want)
	}
	if len(all.msgs) != 3 || !strings.HasPrefix(all.msgs[0].Subject, "[sermo][warning]") ||
		!strings.HasPrefix(all.msgs[1].Subject, "[sermo][critical]") || !strings.Contains(all.msgs[2].Subject, recoveredMessagePrefix) {
		t.Fatalf("unfiltered notifier heard %q", subjects(all.msgs))
	}
	if len(oncall.msgs) != 2 || all.msgs[2].Severity != severity.Critical {
		t.Fatalf("critical notifier heard %q, want the escalation and the recovery", subjects(oncall.msgs))
	}
	if len(errorsOnly.msgs) != 2 {
		t.Fatalf("error notifier heard %q, want the critical escalation and its recovery", subjects(errorsOnly.msgs))
	}
	// The hook runs every firing cycle at the held level, never the eased one.
	if got, want := strings.Join(hookLevels, ","), "warning,warning,critical,critical,critical"; got != want {
		t.Fatalf("hook SERMO_SEVERITY = %s, want %s", got, want)
	}
}

// A notifier whose minimum the episode never reached hears neither the alarm
// nor the recovery.
func TestWatchRecoveryOnlyReachesTheIncidentsAudience(t *testing.T) {
	check := &scriptedCheck{results: []checks.Result{graded(severity.Warning), {Check: "disk", Condition: true}}}
	oncall := &floorNotifier{name: "oncall", floor: severity.Critical}
	w := &Watch{Name: "disk-root", CheckType: checks.CheckTypeStorage, Check: check, Notifiers: []notify.Notifier{oncall}, Emit: func(Event) {}}
	w.RunCycle(context.Background())
	w.RunCycle(context.Background())
	if len(oncall.msgs) != 0 {
		t.Fatalf("a critical-only notifier heard a warning incident: %q", subjects(oncall.msgs))
	}
}

// The escalation mark survives a daemon restart: a restored critical episode
// is not re-announced, and its recovery still reports critical.
func TestWatchEpisodeSeverityPersistsAcrossRestart(t *testing.T) {
	store := openRuleStateStore(t)
	at := time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)
	first := &Watch{
		Name: "disk-root", CheckType: checks.CheckTypeStorage,
		Check:      &scriptedCheck{results: []checks.Result{graded(severity.Critical)}},
		Notifiers:  []notify.Notifier{&fakeNotifier{name: "ops"}},
		StateStore: store,
		Now:        func() time.Time { return at },
		Emit:       func(Event) {},
	}
	first.RunCycle(context.Background())

	settling := NewSettling(nil)
	settling.Reset([]string{SettlingWatchKey("disk-root")})
	n := &fakeNotifier{name: "ops"}
	var events []Event
	second := &Watch{
		Name: "disk-root", CheckType: checks.CheckTypeStorage,
		Check: &scriptedCheck{results: []checks.Result{
			graded(severity.Warning), graded(severity.Warning), {Check: "disk", Condition: true},
		}},
		Notifiers:  []notify.Notifier{n},
		StateStore: store,
		Settling:   settling,
		Now:        func() time.Time { return at.Add(time.Minute) },
		Emit:       func(e Event) { events = append(events, e) },
	}
	for range 3 {
		second.RunCycle(context.Background())
	}
	if countEvents(events, eventKindFiring) != 0 {
		t.Fatalf("restored episode re-announced: %+v", events)
	}
	recovered, ok := findEvent(events, eventKindRecovered)
	if !ok || recovered.Severity != severity.Critical {
		t.Fatalf("recovery = %+v, want critical", recovered)
	}
	if len(n.msgs) != 1 || n.msgs[0].Severity != severity.Critical {
		t.Fatalf("restart notifications = %q, want only the critical recovery", subjects(n.msgs))
	}
}

func findEvent(events []Event, kind string) (Event, bool) {
	for _, e := range events {
		if e.Kind == kind {
			return e, true
		}
	}
	return Event{}, false
}

// A rule alert takes the grade of the check its condition reads, escalates
// when that grade holds, and closes at its mark; the recovery reaches the
// notifiers that heard the alert.
func TestRuleAlertEscalatesWithItsCheck(t *testing.T) {
	h := &workerHarness{cache: map[string]checks.Result{"http": {Check: "http", Severity: severity.Warning}}}
	w := h.worker(alertRuleTree(nil), rules.Policy{}, nil)
	now := t0
	w.Now = func() time.Time { return now }
	oncall := &floorNotifier{name: "oncall", floor: severity.Critical}
	w.Notifiers = map[string]notify.Notifier{"oncall": oncall}
	w.GlobalNotify = []string{"oncall"}

	w.RunCycle(context.Background()) // warning: opens, filtered for oncall
	h.cache = map[string]checks.Result{"http": {Check: "http", Severity: severity.Critical}}
	w.RunCycle(context.Background()) // critical: escalates
	w.RunCycle(context.Background()) // held: nothing new
	h.cache = map[string]checks.Result{"http": {Check: "http", OK: true}}
	w.RunCycle(context.Background())
	now = now.Add(rules.DefaultClearWindow + time.Minute)
	w.RunCycle(context.Background()) // clear window elapsed

	var alerts []string
	for _, e := range h.events {
		if e.Kind == eventKindAlert || e.Kind == eventKindRecovered {
			alerts = append(alerts, e.Kind+"/"+e.Severity.String())
		}
	}
	if got, want := strings.Join(alerts, ","), "alert/warning,alert/critical,recovered/critical"; got != want {
		t.Fatalf("rule events = %s, want %s", got, want)
	}
	if len(oncall.msgs) != 2 || !strings.HasPrefix(oncall.msgs[0].Subject, "[sermo][critical] web:") || !strings.Contains(oncall.msgs[1].Subject, recoveredMessagePrefix) {
		t.Fatalf("oncall heard %q, want the critical escalation and its recovery", subjects(oncall.msgs))
	}
	// A transport paints a recovery as good news by its SERMO_EVENT.
	if oncall.msgs[1].Fields[sermoEnvEvent] != eventKindRecovered {
		t.Fatalf("recovery fields = %v, want SERMO_EVENT=recovered", oncall.msgs[1].Fields)
	}
}

func TestRemediationOutcomeTakesTheTriggerSeverity(t *testing.T) {
	tests := []struct {
		kind    string
		trigger severity.Level
		want    severity.Level
	}{
		{eventKindAction, severity.Critical, severity.Critical},
		{eventKindSuppressed, severity.Warning, severity.Warning},
		{eventKindError, severity.Warning, severity.Error},
		{eventKindError, severity.Critical, severity.Critical},
		{eventKindAction, "", severity.Error},
	}
	for _, tt := range tests {
		if got := remediationSeverity(tt.kind, tt.trigger); got != tt.want {
			t.Errorf("remediationSeverity(%s, %q) = %q, want %q", tt.kind, tt.trigger, got, tt.want)
		}
	}
}

// A service check's health edge opens at the check's grade and escalates at
// once when the grade rises; a grade that eases is held until recovery.
func TestCheckHealthEdgeEscalatesAndHolds(t *testing.T) {
	grades := []severity.Level{severity.Warning, severity.Critical, severity.Warning, ""}
	events := runCycles(t, func(c int) map[string]checks.Result {
		if grades[c-1] == "" {
			return map[string]checks.Result{"service": {Check: "service", OK: true}}
		}
		return map[string]checks.Result{"service": {Check: "service", Severity: grades[c-1]}}
	}, len(grades))
	var got []string
	for _, e := range events {
		got = append(got, e.Kind+"/"+e.Severity.String())
	}
	if want := "firing/warning,firing/critical,recovered/critical"; strings.Join(got, ",") != want {
		t.Fatalf("health events = %v, want %s", got, want)
	}
}

func newTestEventNotifier(t *testing.T, targets ...notify.Notifier) *EventNotifier {
	t.Helper()
	router := NewEventNotifier("host-a", slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	configured := map[string]notify.Notifier{}
	names := make([]string, 0, len(targets))
	for _, target := range targets {
		configured[target.Name()] = target
		names = append(names, target.Name())
	}
	router.Update(config.EventNotification{Targets: names, RepeatInterval: time.Hour}, configured)
	return router
}

// event_notify filters by min_severity without writing a record for a
// filtered notifier, delivers escalations, holds a lower grade, and closes the
// incident at the level each notifier heard.
func TestEventNotifyRoutesIncidentsBySeverity(t *testing.T) {
	all := &floorNotifier{name: "all"}
	oncall := &floorNotifier{name: "oncall", floor: severity.Critical}
	router := newTestEventNotifier(t, all, oncall)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	router.now = func() time.Time { return now }
	ctx := context.Background()

	fire := func(level severity.Level) {
		router.deliver(ctx, Event{Watch: "disk-root", Kind: eventKindFiring, Severity: level, Message: "disk"})
	}
	fire(severity.Warning)
	if len(all.msgs) != 1 || len(oncall.msgs) != 0 {
		t.Fatalf("warning reached all=%d oncall=%d, want 1 and 0", len(all.msgs), len(oncall.msgs))
	}
	if _, found, _ := router.load(formatEventNotifyKey(eventNotifyDimensionWatch, "disk-root", "", "", eventNotifyCategoryHealth), "oncall"); found {
		t.Fatal("a filtered notifier got an incident record: it could later receive an orphan recovery")
	}
	fire(severity.Critical)
	fire(severity.Warning) // held
	fire(severity.Critical)
	if len(all.msgs) != 2 || len(oncall.msgs) != 1 || !strings.HasPrefix(oncall.msgs[0].Subject, "[sermo][critical]") {
		t.Fatalf("escalation reached all=%q oncall=%q", subjects(all.msgs), subjects(oncall.msgs))
	}
	// A reminder repeats the held grade.
	now = now.Add(2 * time.Hour)
	router.remind(ctx)
	if len(all.msgs) != 3 || all.msgs[2].Severity != severity.Critical || !strings.Contains(all.msgs[2].Subject, "continues") {
		t.Fatalf("reminder = %q", subjects(all.msgs))
	}
	router.deliver(ctx, Event{Watch: "disk-root", Kind: eventKindRecovered, Message: "ok"})
	last := func(n *floorNotifier) notify.Message { return n.msgs[len(n.msgs)-1] }
	if last(all).Severity != severity.Critical || last(oncall).Severity != severity.Critical || !strings.Contains(last(oncall).Subject, "recovered") {
		t.Fatalf("recoveries all=%q oncall=%q, want both at critical", last(all).Subject, last(oncall).Subject)
	}
	if last(oncall).Fields[sermoEnvSeverity] != string(severity.Critical) {
		t.Fatalf("recovery fields = %v", last(oncall).Fields)
	}
}

// A reload that raises a notifier's minimum above an open incident silences
// its reminders instead of repeating them every scan.
func TestEventNotifyReminderRespectsARaisedMinimum(t *testing.T) {
	n := &floorNotifier{name: "ops"}
	router := newTestEventNotifier(t, n)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	router.now = func() time.Time { return now }
	router.deliver(context.Background(), Event{Watch: "disk-root", Kind: eventKindFiring, Severity: severity.Warning})
	n.floor = severity.Error
	now = now.Add(2 * time.Hour)
	router.remind(context.Background())
	if len(n.msgs) != 1 {
		t.Fatalf("reminders after raising the minimum = %q", subjects(n.msgs))
	}
	rec, _, _ := router.load(formatEventNotifyKey(eventNotifyDimensionWatch, "disk-root", "", "", eventNotifyCategoryHealth), "ops")
	if !rec.LastSentAt.Equal(now) {
		t.Fatalf("silenced reminder did not advance: %+v", rec)
	}
}

// A remediation held back by its cooldown still announces an escalation
// through the rule's alert, and the recovery reaches the notifiers that heard
// that escalation.
func TestSuppressedRemediationStillAnnouncesEscalation(t *testing.T) {
	h := &workerHarness{
		opResult: operation.Result{Status: operation.ResultOK},
		cache:    map[string]checks.Result{"http": {Check: "http", Severity: severity.Warning}},
	}
	tree := map[string]any{"rules": map[string]any{
		"restart-if-down": map[string]any{
			"type": "remediation",
			"if":   map[string]any{"failed": map[string]any{"check": "http"}},
			"then": map[string]any{"actions": []any{
				map[string]any{"type": "alert", "message": "http is down"},
				map[string]any{"type": "restart"},
			}},
		},
	}}
	w := h.worker(tree, rules.Policy{Cooldown: 10 * time.Minute}, nil)
	all := &floorNotifier{name: "all"}
	oncall := &floorNotifier{name: "oncall", floor: severity.Critical}
	w.Notifiers = map[string]notify.Notifier{"all": all, "oncall": oncall}
	w.GlobalNotify = []string{"all", "oncall"}

	w.RunCycle(context.Background()) // warning: alert + restart
	h.cache = map[string]checks.Result{"http": {Check: "http", Severity: severity.Critical}}
	w.RunCycle(context.Background()) // critical: restart held by the cooldown
	h.cache = map[string]checks.Result{"http": {Check: "http", OK: true}}
	w.RunCycle(context.Background()) // recovered

	if len(h.ops) != 1 {
		t.Fatalf("operations = %v, want the single restart the cooldown allowed", h.ops)
	}
	if len(oncall.msgs) != 2 || !strings.HasPrefix(oncall.msgs[0].Subject, "[sermo][critical]") ||
		!strings.Contains(oncall.msgs[1].Subject, recoveredMessagePrefix) || oncall.msgs[1].Severity != severity.Critical {
		t.Fatalf("oncall heard %q, want the critical escalation and its recovery", subjects(oncall.msgs))
	}
	if len(all.msgs) != 3 {
		t.Fatalf("unfiltered notifier heard %q, want warning, critical and recovery", subjects(all.msgs))
	}
}

// An escalation that panic mode kept quiet was never heard, so the recovery
// goes out at the level that was: a critical-only notifier hears nothing.
func TestWatchRecoverySkipsAnEscalationNobodyHeard(t *testing.T) {
	panicking := false
	check := &scriptedCheck{results: []checks.Result{graded(severity.Warning), graded(severity.Critical), {Check: "disk", Condition: true}}}
	all := &floorNotifier{name: "all"}
	oncall := &floorNotifier{name: "oncall", floor: severity.Critical}
	w := &Watch{
		Name: "disk-root", CheckType: checks.CheckTypeStorage, Check: check,
		Notifiers: []notify.Notifier{all, oncall},
		InPanic:   func() bool { return panicking },
		Emit:      func(Event) {},
	}
	w.RunCycle(context.Background()) // warning, announced
	panicking = true
	w.RunCycle(context.Background()) // critical, suppressed by panic mode
	panicking = false
	w.RunCycle(context.Background()) // recovered
	if len(oncall.msgs) != 0 {
		t.Fatalf("critical notifier heard %q, want nothing: it never heard the incident", subjects(oncall.msgs))
	}
	if len(all.msgs) != 2 || all.msgs[1].Severity != severity.Warning || !strings.Contains(all.msgs[1].Subject, recoveredMessagePrefix) {
		t.Fatalf("unfiltered notifier heard %q, want the warning and a warning recovery", subjects(all.msgs))
	}
}

// An episode recorded before severity was kept, and found recovered on the
// first cycle after the upgrade, is graded by the watch rather than read as an
// error.
func TestLegacyRestoredEpisodeRecoversAtTheWatchGrade(t *testing.T) {
	store := openRuleStateStore(t)
	at := time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)
	if err := store.SetWatchRuntimeState("disk-root", watchStateDefaultSlot, state.WatchRuntimeRecord{Firing: true, LastNotifyAt: at}); err != nil {
		t.Fatal(err)
	}
	settling := NewSettling(nil)
	settling.Reset([]string{SettlingWatchKey("disk-root")})
	n := &fakeNotifier{name: "ops"}
	var events []Event
	w := &Watch{
		Name: "disk-root", CheckType: checks.CheckTypeStorage, Severity: severity.Warning,
		Check:      &scriptedCheck{results: []checks.Result{{Check: "disk", Condition: true}}},
		Notifiers:  []notify.Notifier{n},
		StateStore: store,
		Settling:   settling,
		Now:        func() time.Time { return at.Add(time.Minute) },
		Emit:       func(e Event) { events = append(events, e) },
	}
	w.RunCycle(context.Background())
	recovered, ok := findEvent(events, eventKindRecovered)
	if !ok || recovered.Severity != severity.Warning {
		t.Fatalf("recovery = %+v, want warning", recovered)
	}
	if len(n.msgs) != 1 || !strings.HasPrefix(n.msgs[0].Subject, "[sermo][warning]") {
		t.Fatalf("recovery notification = %q, want a warning", subjects(n.msgs))
	}
}

// An advisory watch whose probe becomes unavailable raises an error graded
// below an outage. It stays a health incident, as the advisory kind it replaced
// was: the probe's return closes it and reaches the notifier that heard it.
func TestEventNotifyClosesAnAdvisoryAvailabilityIncident(t *testing.T) {
	n := &floorNotifier{name: "ops"}
	router := newTestEventNotifier(t, n)
	ctx := context.Background()
	router.deliver(ctx, Event{Watch: "api", Kind: eventKindError, Severity: severity.Warning, Check: watchAvailabilityCheck, Message: checkUnavailablePrefix + "timeout"})
	router.deliver(ctx, Event{Watch: "api", Kind: eventKindRecovered, Check: watchAvailabilityCheck, Message: "check available: ok"})
	if len(n.msgs) != 2 || !strings.HasPrefix(n.msgs[0].Subject, "[sermo][warning]") ||
		!strings.Contains(n.msgs[1].Subject, "recovered") || n.msgs[1].Severity != severity.Warning {
		t.Fatalf("availability incident = %q, want the warning and its recovery", subjects(n.msgs))
	}
}

// A dry-run notification reaches the console only, so it defines no audience:
// after a reload turns dry-run off mid-episode, the recovery must not reach a
// notifier that never heard the incident — for a watch and for a rule alike.
func TestDryRunNotificationDefinesNoRecoveryAudience(t *testing.T) {
	check := &scriptedCheck{results: []checks.Result{graded(severity.Critical), {Check: "disk", Condition: true}}}
	mail := &floorNotifier{name: "mail"}
	w := &Watch{
		Name: "disk-root", CheckType: checks.CheckTypeStorage, Check: check,
		Notifiers: []notify.Notifier{mail}, DryRun: true, Emit: func(Event) {},
	}
	w.RunCycle(context.Background())
	w.DryRun = false
	w.RunCycle(context.Background())
	if len(mail.msgs) != 0 {
		t.Fatalf("watch: mail heard %q, want nothing", subjects(mail.msgs))
	}

	h := &workerHarness{cache: map[string]checks.Result{"http": {Check: "http", Severity: severity.Critical}}}
	worker := h.worker(alertRuleTree(nil), rules.Policy{}, nil)
	now := t0
	worker.Now = func() time.Time { return now }
	worker.Notifiers = map[string]notify.Notifier{"mail": mail}
	worker.GlobalNotify = []string{"mail"}
	worker.DryRun = true
	worker.RunCycle(context.Background())
	worker.DryRun = false
	h.cache = map[string]checks.Result{"http": {Check: "http", OK: true}}
	worker.RunCycle(context.Background())
	now = now.Add(rules.DefaultClearWindow + time.Minute)
	worker.RunCycle(context.Background())
	if _, ok := findEvent(h.events, eventKindRecovered); !ok {
		t.Fatalf("rule never recovered: %+v", h.events)
	}
	if len(mail.msgs) != 0 {
		t.Fatalf("rule: mail heard %q, want nothing", subjects(mail.msgs))
	}
}

// A service check episode restored after a restart was announced before it, at
// a grade its snapshot only bounds from below: the first failing observation
// raises the grade silently, and the recovery closes the episode at it.
func TestRestoredCheckEpisodeAdoptsItsGradeSilently(t *testing.T) {
	var events []Event
	cycle := 0
	w := &Worker{
		Service:       "web",
		checkEpisodes: map[string]checkEpisode{"service": {held: severity.Error, restored: true}},
		Checks: func(context.Context, checks.Deps) map[string]checks.Result {
			cycle++
			if cycle == 3 {
				return map[string]checks.Result{"service": {Check: "service", OK: true}}
			}
			return map[string]checks.Result{"service": {Check: "service", Severity: severity.Critical}}
		},
		Emit: func(e Event) { events = append(events, e) },
	}
	for range 3 {
		w.RunCycle(context.Background())
	}
	var got []string
	for _, e := range events {
		got = append(got, e.Kind+"/"+e.Severity.String())
	}
	if want := "recovered/critical"; strings.Join(got, ",") != want {
		t.Fatalf("health events = %v, want %s", got, want)
	}
}

// A RAID watch announces its own recovery (on_good) instead of a then.notify
// recovery, so the healthy transition is graded at the gravest failure it
// follows: a channel that heard the degraded array hears it healthy again,
// while routine news on a healthy array stays info.
func TestRaidRecoveryTransitionReachesTheFailuresAudience(t *testing.T) {
	transitions := func(ts ...checks.RaidTransition) map[string]any {
		return map[string]any{checks.DataKeyRaidTransitions: ts}
	}
	check := &scriptedCheck{results: []checks.Result{
		{Check: "raid", Severity: severity.Critical, Data: transitions(checks.RaidTransition{Event: checks.RaidNotifyOnDegraded, Array: "md0"})},
		{Check: "raid", OK: true, Data: transitions(checks.RaidTransition{Event: checks.RaidNotifyOnGood, Array: "md0"})},
		{Check: "raid", OK: true, Data: transitions(checks.RaidTransition{Event: checks.RaidNotifyOnArrayChange, Array: "md0", Field: "state", Old: "clean", New: "active"})},
	}}
	oncall := &floorNotifier{name: "oncall", floor: severity.Critical}
	w := &Watch{
		Name: "raid-md0", CheckType: checks.CheckTypeRAID, Check: check, FireOnFail: true,
		Notifiers: []notify.Notifier{oncall},
		RaidNotifyEvents: map[string]bool{
			checks.RaidNotifyOnDegraded: true, checks.RaidNotifyOnGood: true, checks.RaidNotifyOnArrayChange: true,
		},
		Emit: func(Event) {},
	}
	for range 3 {
		w.RunCycle(context.Background())
	}
	if len(oncall.msgs) != 2 || !strings.Contains(oncall.msgs[0].Subject, "degraded") ||
		!strings.Contains(oncall.msgs[1].Subject, "healthy") || oncall.msgs[1].Severity != severity.Critical {
		t.Fatalf("oncall heard %q, want the degraded array and its recovery", subjects(oncall.msgs))
	}
	if oncall.msgs[1].Fields[sermoEnvEvent] != eventKindRecovered || oncall.msgs[0].Fields[sermoEnvEvent] == eventKindRecovered {
		t.Fatal("only the healthy transition is marked as the recovery")
	}
}

// A rebuild outlasts many daemon restarts (a package update restarts sermod),
// so the level a RAID failure was delivered at persists with the watch.
func TestRaidRecoveryTransitionLevelSurvivesARestart(t *testing.T) {
	store := openRuleStateStore(t)
	raidWatch := func(result checks.Result, n notify.Notifier) *Watch {
		return &Watch{
			Name: "raid-md0", CheckType: checks.CheckTypeRAID, FireOnFail: true,
			Check:      &scriptedCheck{results: []checks.Result{result}},
			Notifiers:  []notify.Notifier{n},
			StateStore: store,
			RaidNotifyEvents: map[string]bool{
				checks.RaidNotifyOnDegraded: true, checks.RaidNotifyOnGood: true,
			},
			Emit: func(Event) {},
		}
	}
	transition := func(event string) map[string]any {
		return map[string]any{checks.DataKeyRaidTransitions: []checks.RaidTransition{{Event: event, Array: "md0"}}}
	}
	before := &floorNotifier{name: "oncall", floor: severity.Critical}
	raidWatch(checks.Result{Check: "raid", Severity: severity.Critical, Data: transition(checks.RaidNotifyOnDegraded)}, before).RunCycle(context.Background())
	after := &floorNotifier{name: "oncall", floor: severity.Critical}
	raidWatch(checks.Result{Check: "raid", OK: true, Data: transition(checks.RaidNotifyOnGood)}, after).RunCycle(context.Background())
	if len(before.msgs) != 1 || len(after.msgs) != 1 || after.msgs[0].Severity != severity.Critical {
		t.Fatalf("oncall heard %q then %q, want the failure and, after the restart, its recovery", subjects(before.msgs), subjects(after.msgs))
	}
}

// A process_policy incident whose violation panic mode kept quiet was never
// heard, so its aggregate recovery stays quiet too.
func TestProcessPolicyRecoveryOnlyForAnIncidentItsNotifiersHeard(t *testing.T) {
	panicking := true
	ops := &floorNotifier{name: "ops"}
	rogue := ProcInfo{PID: 42, UID: 70, Exe: "/usr/bin/bash", ExeOK: true, StartTicks: 100}
	w, _, _ := testProcessPolicyWatcher(t,
		&fakeProcSampler{cycles: [][]ProcInfo{{rogue}, {}, {rogue}, {}}},
		map[string]any{"postgres": map[string]any{checks.CheckKeyExe: "/usr/bin/postgres"}},
	)
	w.notifiers = []notify.Notifier{ops}
	w.inPanic = func() bool { return panicking }
	w.runCycle(context.Background()) // violation, notification suppressed
	panicking = false
	w.runCycle(context.Background()) // no violations
	if len(ops.msgs) != 0 {
		t.Fatalf("ops heard %q for an incident it never heard", subjects(ops.msgs))
	}
	w.runCycle(context.Background()) // violation, notified
	w.runCycle(context.Background()) // no violations: the recovery goes out
	if len(ops.msgs) != 2 || !strings.Contains(ops.msgs[1].Subject, recoveredMessagePrefix) {
		t.Fatalf("ops heard %q, want the violation and its recovery", subjects(ops.msgs))
	}
}

// A delivery record an older binary wrote after the migration reads as the
// advisory firing it was; a graded record is never rewritten.
func TestNormalizeLegacyNotifyRecord(t *testing.T) {
	legacy := normalizeLegacyNotifyRecord(state.EventNotifyRecord{Phase: legacyEventKindWarning})
	if legacy.Phase != eventKindFiring || legacy.Severity != string(severity.Warning) {
		t.Fatalf("legacy record = %+v, want an advisory firing", legacy)
	}
	graded := state.EventNotifyRecord{Phase: eventKindFiring, Severity: string(severity.Critical)}
	if got := normalizeLegacyNotifyRecord(graded); got != graded {
		t.Fatalf("graded record rewritten: %+v", got)
	}
}
