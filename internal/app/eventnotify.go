package app

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sermo/internal/config"
	"sermo/internal/notify"
	"sermo/internal/process"
	"sermo/internal/severity"
	"sermo/internal/state"
)

const (
	eventNotifyQueueSize      = 256
	eventNotifyTimeout        = 15 * time.Second
	eventNotifyScanInterval   = time.Minute
	eventNotifyLogInterval    = time.Minute
	eventNotifyErrorInterval  = 24 * time.Hour
	eventNotifyPhaseRecovered = "recovered"
)

// Incident key dimensions and the category of incidents a recovery closes.
const (
	eventNotifyDimensionDaemon  = "daemon"
	eventNotifyDimensionService = "service"
	eventNotifyDimensionWatch   = "watch"
	eventNotifyDimensionApp     = "app"
	eventNotifyCategoryHealth   = "health"
	eventNotifyKeySeparator     = ':'
	eventNotifyKeyFields        = 5
)

// EventNotifyStore owns durable delivery edges independently of the event log.
type EventNotifyStore interface {
	EventNotifyState(incidentKey, notifier string) (state.EventNotifyRecord, bool, error)
	SetEventNotifyState(record state.EventNotifyRecord) error
	DueEventNotifyStates(notifier string, before time.Time) ([]state.EventNotifyRecord, error)
	ActiveEventNotifyIncidents() ([]string, error)
	DeleteEventNotifyIncident(incidentKey string) error
}

// EventNotifier routes daemon alarm events to named notifiers. Its queue keeps
// external transport latency out of monitoring cycles. The state store ensures
// a daemon restart does not turn an ongoing incident into a new alert.
type EventNotifier struct {
	host    string
	logger  *slog.Logger
	inPanic func() bool
	store   EventNotifyStore
	now     func() time.Time
	queue   chan Event
	mu      sync.RWMutex
	targets []notify.Notifier
	policy  config.EventNotification
	// deliveryMu serializes delivery and retention across config generations.
	deliveryMu sync.Mutex
	// memory overlays records not yet durable; a failed write must never undo
	// a successful external delivery. Without a store it holds all records.
	memory         map[string]state.EventNotifyRecord
	persistRetryAt time.Time
	persistFailed  bool
	queueDropped   atomic.Uint64
	queueLogAt     atomic.Int64
	// sleep paces delivery retries; nil uses a real, cancellable timer.
	sleep         func(time.Duration)
	bursts        map[string]*eventNotifyBurst
	scope         *eventNotifyScope
	readLogAt     time.Time
	capacityLogAt time.Time
}

// eventNotifyRetryDelays are the pauses before each retry of a delivery that
// failed for a reason that may pass (a lookup timeout, a 5xx, a rate limit):
// the queue runs outside the monitoring cycles, so waiting costs no check.
var eventNotifyRetryDelays = []time.Duration{2 * time.Second, 5 * time.Second}

// NewEventNotifier creates a bounded, asynchronous route for daemon alarms.
func NewEventNotifier(host string, logger *slog.Logger, inPanic func() bool, store EventNotifyStore) *EventNotifier {
	return &EventNotifier{
		host: host, logger: logger, inPanic: inPanic, store: store,
		now: time.Now, queue: make(chan Event, eventNotifyQueueSize),
		memory: make(map[string]state.EventNotifyRecord),
		bursts: make(map[string]*eventNotifyBurst),
	}
}

// Update replaces the validated global selection on a successful config reload.
func (n *EventNotifier) Update(policy config.EventNotification, configured map[string]notify.Notifier) {
	if n == nil {
		return
	}
	targets := make([]notify.Notifier, 0, len(policy.Targets))
	for _, name := range policy.Targets {
		if target := configured[name]; target != nil {
			targets = append(targets, target)
		}
	}
	n.mu.Lock()
	n.targets = targets
	n.policy = policy
	n.mu.Unlock()
}

// Emit is nonblocking and must be wired into the daemon's shared event emitter.
func (n *EventNotifier) Emit(e Event) {
	if n == nil || !eventNeedsNotification(e) {
		return
	}
	n.mu.RLock()
	enabled := len(n.targets) > 0
	n.mu.RUnlock()
	if !enabled {
		return
	}
	select {
	case n.queue <- e:
	default:
		n.queueDropped.Add(1)
		now := time.Now().UnixNano()
		last := n.queueLogAt.Load()
		if now-last >= int64(eventNotifyLogInterval) && n.queueLogAt.CompareAndSwap(last, now) {
			n.logger.Error("event notification queue full", "dropped", n.queueDropped.Swap(0))
		}
	}
}

// Run delivers queued alerts and scans due reminders without blocking workers.
func (n *EventNotifier) Run(ctx context.Context) {
	if n == nil {
		return
	}
	ticker := time.NewTicker(eventNotifyScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-n.queue:
			n.deliver(ctx, e)
		case <-ticker.C:
			n.remind(ctx)
		}
	}
}

// Retain forgets open incidents that no target of the running generation can
// emit again: a removed or renamed service, check, rule, watch or app never
// sends the recovery that would close them, so with repeat_interval they would
// be reminded forever. It runs when a generation starts, before its workers,
// and also removes pending in-memory delivery records.
func (n *EventNotifier) Retain(scope eventNotifyScope) {
	if n == nil {
		return
	}
	n.deliveryMu.Lock()
	defer n.deliveryMu.Unlock()
	n.scope = &scope
	targets, _ := n.deliveryConfig()
	enabled := make(map[string]bool, len(targets))
	for _, target := range targets {
		enabled[target.Name()] = true
	}
	for name := range n.bursts {
		if !enabled[name] {
			delete(n.bursts, name)
		}
	}
	for _, burst := range n.bursts {
		for key, pending := range burst.pending {
			if pending.record.Active && !pending.notice && !scope.live(pending.record.IncidentKey) {
				delete(burst.pending, key)
			}
		}
	}
	for key, rec := range n.memory {
		if rec.Active && !scope.live(rec.IncidentKey) {
			delete(n.memory, key)
		}
	}
	if n.store == nil {
		return
	}
	keys, err := n.store.ActiveEventNotifyIncidents()
	if err != nil {
		n.logger.Error("list open event notifications", "error", err)
		return
	}
	for _, key := range keys {
		if scope.live(key) {
			continue
		}
		if err := n.store.DeleteEventNotifyIncident(key); err != nil {
			n.logger.Error("drop orphaned event notification", "incident", key, "error", err)
		}
	}
}

// eventNotifyScope is what the running generation can still emit events for.
type eventNotifyScope struct {
	services map[string]eventNotifyServiceScope
	watches  map[string]bool
	apps     map[string]bool
}

type eventNotifyServiceScope struct {
	rules  map[string]bool
	checks map[string]bool
}

func eventNotifyScopeOf(workers []*Worker, watches []*Watch) eventNotifyScope {
	scope := eventNotifyScope{
		services: make(map[string]eventNotifyServiceScope, len(workers)),
		watches:  make(map[string]bool, len(watches)),
		apps:     map[string]bool{},
	}
	for _, w := range workers {
		if w == nil {
			continue
		}
		svc := eventNotifyServiceScope{rules: make(map[string]bool, len(w.Rules)), checks: w.checkNames}
		for i := range w.Rules {
			svc.rules[w.Rules[i].Name] = true
		}
		scope.services[w.Service] = svc
	}
	for _, w := range watches {
		switch {
		case w == nil:
		case w.App != "":
			// Watch.emit records an app watch's events on the app dimension.
			scope.apps[w.App] = true
		default:
			scope.watches[w.Name] = true
		}
	}
	return scope
}

// live reports whether an open incident still belongs to a running target. A
// service incident is opened only by a check or a rule of that service, so a
// key naming neither (a pre-notice lock-reclaim alert) cannot be closed either.
// A key it cannot parse is kept rather than guessed at.
func (s eventNotifyScope) live(key string) bool {
	parts, ok := parseEventNotifyKey(key)
	if !ok {
		return true
	}
	dimension, name, rule, check := parts[0], parts[1], parts[2], parts[3]
	switch dimension {
	case eventNotifyDimensionService:
		svc, ok := s.services[name]
		return ok && ((rule != "" && svc.rules[rule]) || (check != "" && svc.checks[check]))
	case eventNotifyDimensionWatch:
		return s.watches[name]
	case eventNotifyDimensionApp:
		return s.apps[name]
	default:
		return true
	}
}

func formatEventNotifyKey(dimension, name, rule, check, category string) string {
	return fmt.Sprintf("%q:%q:%q:%q:%q", dimension, name, rule, check, category)
}

// parseEventNotifyKey splits a key built by formatEventNotifyKey.
func parseEventNotifyKey(key string) ([]string, bool) {
	parts := make([]string, 0, eventNotifyKeyFields)
	for rest := key; ; {
		quoted, err := strconv.QuotedPrefix(rest)
		if err != nil {
			return nil, false
		}
		part, err := strconv.Unquote(quoted)
		if err != nil {
			return nil, false
		}
		parts = append(parts, part)
		rest = rest[len(quoted):]
		if rest == "" {
			break
		}
		if rest[0] != eventNotifyKeySeparator {
			return nil, false
		}
		rest = rest[1:]
	}
	return parts, len(parts) == eventNotifyKeyFields
}

func (n *EventNotifier) deliveryConfig() ([]notify.Notifier, time.Duration) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return append([]notify.Notifier(nil), n.targets...), n.policy.RepeatInterval
}

func (n *EventNotifier) deliver(ctx context.Context, e Event) {
	n.deliveryMu.Lock()
	defer n.deliveryMu.Unlock()
	if n.inPanic != nil && n.inPanic() {
		return
	}
	targets, interval := n.deliveryConfig()
	key, active, phase := eventNotifyIdentity(e)
	level := e.level()
	now := n.now()
	for _, target := range targets {
		if e.Notice {
			// Its producer already reports each occurrence once, and no recovery
			// edge follows, so persisting it would open an incident that nothing
			// closes: it would silence the next notice and be reminded forever.
			if notify.Accepts(target, level) {
				n.dispatch(ctx, target, e, n.message(e, level), level == severity.Critical)
			}
			continue
		}
		if !n.canRemember(key, target.Name()) {
			continue
		}
		rec, found, err := n.load(key, target.Name())
		if err != nil {
			if n.readLogAt.IsZero() || now.Sub(n.readLogAt) >= eventNotifyLogInterval {
				n.readLogAt = now
				n.logger.Error("load event notification state", "notifier", target.Name(), "error", err)
			}
			continue
		}
		n.reconcilePending(target.Name(), key, e, rec, found)
		sent, ok := n.deliverTo(ctx, target, e, deliveryEdge{active: active, phase: phase, level: level, rec: rec, found: found, now: now, interval: interval})
		if !ok {
			continue
		}
		rec = eventNotifyRecord(e, target.Name(), sent)
		rec.LastSentAt = now
		n.save(rec)
	}
}

// deliveryEdge is one event's incident edge at one notifier.
type deliveryEdge struct {
	active   bool
	phase    string
	level    severity.Level
	rec      state.EventNotifyRecord
	found    bool
	now      time.Time
	interval time.Duration
}

// deliverTo sends e to target when its incident edge calls for it and
// returns the message sent, whose severity the record keeps.
//
// A recovery reaches exactly the notifiers told of the incident, at the
// gravest level each one heard (its record's severity): it never passes the
// min_severity filter on its own grade. Any other event is filtered first — a notifier below its minimum gets
// no record, so it can never receive an orphan reminder or recovery — and
// then delivered on a new incident, an escalation, or a reminder; a lower
// grade inside an open incident is held.
func (n *EventNotifier) deliverTo(ctx context.Context, target notify.Notifier, e Event, edge deliveryEdge) (notify.Message, bool) {
	if e.Kind == eventKindRecovered {
		if !edge.found || !edge.rec.Active {
			return notify.Message{}, false
		}
		msg := n.message(e, severity.Level(edge.rec.Severity))
		return msg, n.dispatch(ctx, target, e, msg, false)
	}
	if !notify.Accepts(target, edge.level) || !shouldDeliverEvent(edge) {
		return notify.Message{}, false
	}
	held := edge.level
	if edge.active && edge.found && edge.rec.Active {
		held = severity.Max(severity.Level(edge.rec.Severity), edge.level)
	}
	msg := n.message(e, edge.level)
	priority := edge.level == severity.Critical || (edge.found && edge.level.Rank() > severity.Level(edge.rec.Severity).Resolved().Rank())
	if !n.dispatch(ctx, target, e, msg, priority) {
		return notify.Message{}, false
	}
	msg.Severity = held
	return msg, true
}

func (n *EventNotifier) send(ctx context.Context, target notify.Notifier, msg notify.Message) bool {
	if err := n.sendWithRetry(ctx, target, msg); err != nil {
		n.logger.Error("event notification failed", "notifier", target.Name(),
			"subject", msg.Subject, "error", err)
		return false
	}
	return true
}

func (n *EventNotifier) remind(ctx context.Context) {
	n.deliveryMu.Lock()
	defer n.deliveryMu.Unlock()
	n.retryPersistence()
	if n.inPanic != nil && n.inPanic() {
		return
	}
	targets, interval := n.deliveryConfig()
	n.flushSummaries(ctx, targets)
	if interval <= 0 {
		return
	}
	now := n.now()
	for _, target := range targets {
		due, err := n.due(target.Name(), now.Add(-interval))
		if err != nil {
			n.logger.Error("load due event notifications", "notifier", target.Name(), "error", err)
			continue
		}
		for _, rec := range due {
			if !n.canRemember(rec.IncidentKey, target.Name()) {
				break
			}
			level := severity.Level(rec.Severity).Resolved()
			rec.LastSentAt = now
			if !notify.Accepts(target, level) {
				// A reload raised this notifier's minimum above the incident:
				// keep the record quiet rather than remind it every scan.
				n.save(rec)
				continue
			}
			msg := notify.Message{Subject: rec.Subject + " (continues)", Body: rec.Body, Severity: level}
			if !n.dispatchReminder(ctx, target, rec, msg) {
				continue
			}
			n.save(rec)
		}
	}
}

// sendWithRetry delivers msg, retrying a failure that may pass. A notifier
// that rejected the message (a wrong webhook, a bad token) is not retried.
func (n *EventNotifier) sendWithRetry(ctx context.Context, target notify.Notifier, msg notify.Message) error {
	err := sendEventNotification(ctx, target, msg)
	for _, delay := range eventNotifyRetryDelays {
		if !notify.IsTemporary(err) {
			return err
		}
		if process.Wait(ctx, n.sleep, delay) != nil {
			return err
		}
		err = sendEventNotification(ctx, target, msg)
	}
	return err
}

func sendEventNotification(ctx context.Context, target notify.Notifier, msg notify.Message) error {
	sendCtx, cancel := context.WithTimeout(ctx, eventNotifyTimeout)
	defer cancel()
	if err := target.Send(sendCtx, msg); err != nil {
		return fmt.Errorf("send event notification: %w", err)
	}
	return nil
}

// normalizeLegacyNotifyRecord reads a delivery record an older binary wrote
// after the migration: its advisory phase is a firing graded warning.
func normalizeLegacyNotifyRecord(rec state.EventNotifyRecord) state.EventNotifyRecord {
	if rec.Phase == legacyEventKindWarning && rec.Severity == "" {
		rec.Phase, rec.Severity = eventKindFiring, severity.Warning.String()
	}
	return rec
}

// eventNotifyIdentity groups all PID-level process-policy events under their
// watch, while independent service checks and rules retain separate episodes.
func eventNotifyIdentity(e Event) (key string, active bool, phase string) {
	dimension, name := eventNotifyDimensionDaemon, ""
	switch {
	case e.Service != "":
		dimension, name = eventNotifyDimensionService, e.Service
	case e.Watch != "":
		dimension, name = eventNotifyDimensionWatch, e.Watch
	case e.App != "":
		dimension, name = eventNotifyDimensionApp, e.App
	}
	category := eventNotifyCategoryHealth
	phase = e.Kind
	switch e.Kind {
	case eventKindRecovered:
		phase = eventNotifyPhaseRecovered
	case eventKindDryRun, eventKindSuppressed, eventKindAction:
		phase = eventKindFiring
	case eventKindError, eventKindHookFail, eventKindExpandFailed, eventKindKillFailed, eventKindMakeStepFailed, eventKindRemountFailed:
		// An advisory incident's error stays a health incident, so the
		// recovery that follows it closes it.
		if e.advisoryError() {
			break
		}
		category = e.Kind
		if e.Action != "" {
			category += ":" + e.Action
		}
	}
	active = category == eventNotifyCategoryHealth && phase != eventNotifyPhaseRecovered
	return formatEventNotifyKey(dimension, name, e.Rule, e.Check, category), active, phase
}

// shouldDeliverEvent decides whether a non-recovery event opens, escalates or
// reminds its incident at one notifier. An operational error is paced (once per
// interval, sooner when it grows graver); a health incident delivers when it
// opens, when its grade rises above what the notifier last heard, or when the
// same grade arrives in another phase (a rule's alert, then its remediation
// outcome), and holds a lower grade silently.
func shouldDeliverEvent(edge deliveryEdge) bool {
	rec := edge.rec
	if !edge.found {
		return true
	}
	held := severity.Level(rec.Severity).Resolved()
	if !edge.active {
		interval := edge.interval
		if interval <= 0 {
			interval = eventNotifyErrorInterval
		}
		return edge.level.Rank() > held.Rank() || edge.now.Sub(rec.LastSentAt) >= interval
	}
	switch {
	case !rec.Active, edge.level.Rank() > held.Rank():
		return true
	case edge.level.Rank() < held.Rank():
		return false
	case rec.Phase != edge.phase:
		return true
	}
	return edge.interval > 0 && edge.now.Sub(rec.LastSentAt) >= edge.interval
}

func (n *EventNotifier) message(e Event, level severity.Level) notify.Message {
	level = level.Resolved()
	var subject strings.Builder
	subject.WriteString(subjectPrefix(level) + " " + n.host)
	for _, part := range []string{e.Service, e.Watch, e.App} {
		if part != "" {
			subject.WriteString(" " + part)
			break
		}
	}
	subject.WriteString(": " + e.Kind)
	var details []string
	for _, field := range [][2]string{{"check", e.Check}, {"rule", e.Rule}, {"action", e.Action}, {"status", e.Status}, {"message", e.Message}, {"output", e.Output}} {
		if field[1] != "" {
			details = append(details, field[0]+": "+field[1])
		}
	}
	fields := map[string]string{sermoEnvSeverity: level.String(), sermoEnvEvent: e.Kind}
	for field, value := range map[string]string{sermoEnvService: e.Service, sermoEnvWatch: e.Watch, sermoEnvApp: e.App, sermoEnvRule: e.Rule} {
		if value != "" {
			fields[field] = value
		}
	}
	return notify.Message{Subject: subject.String(), Body: strings.Join(details, "\n"), Fields: fields, Severity: level}
}

func eventNeedsNotification(e Event) bool {
	switch e.Kind {
	case eventKindFiring, eventKindRecovered, eventKindAlert,
		eventKindError, eventKindHookFail, eventKindExpandFailed,
		eventKindKillFailed, eventKindMakeStepFailed, eventKindRemountFailed:
		return true
	case eventKindDryRun, eventKindSuppressed, eventKindAction:
		// A service check claimed by a remediation rule has no standalone firing
		// event, so the rule outcome is its alarm, including in dry-run mode.
		return e.Service != "" && e.Rule != "" && e.Action != ""
	default:
		return false
	}
}
