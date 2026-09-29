package app

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"sermo/internal/config"
	"sermo/internal/notify"
	"sermo/internal/state"
)

const (
	eventNotifyQueueSize      = 256
	eventNotifyTimeout        = 15 * time.Second
	eventNotifyScanInterval   = time.Minute
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
	// memory is used only by tests that do not provide a persistent store.
	memory map[string]state.EventNotifyRecord
}

// NewEventNotifier creates a bounded, asynchronous route for daemon alarms.
func NewEventNotifier(host string, logger *slog.Logger, inPanic func() bool, store EventNotifyStore) *EventNotifier {
	return &EventNotifier{
		host: host, logger: logger, inPanic: inPanic, store: store,
		now: time.Now, queue: make(chan Event, eventNotifyQueueSize),
		memory: make(map[string]state.EventNotifyRecord),
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
		n.logger.Error("event notification queue full", eventFieldKind, e.Kind,
			eventFieldService, e.Service, eventFieldWatch, e.Watch, eventFieldApp, e.App)
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
// and only against the persistent store; the in-memory fallback is owned by
// the Run goroutine and used only in tests.
func (n *EventNotifier) Retain(scope eventNotifyScope) {
	if n == nil || n.store == nil {
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
	if n.inPanic != nil && n.inPanic() {
		return
	}
	targets, interval := n.deliveryConfig()
	key, active, phase := eventNotifyIdentity(e)
	msg := n.message(e)
	now := n.now()
	for _, target := range targets {
		if e.Notice {
			// Its producer already reports each occurrence once, and no recovery
			// edge follows, so persisting it would open an incident that nothing
			// closes: it would silence the next notice and be reminded forever.
			n.send(ctx, target, e, msg)
			continue
		}
		rec, found, err := n.load(key, target.Name())
		if err != nil {
			n.logger.Error("load event notification state", "notifier", target.Name(), "error", err)
			continue
		}
		if !shouldDeliverEvent(e.Kind, active, phase, rec, found, now, interval) {
			continue
		}
		if !n.send(ctx, target, e, msg) {
			continue
		}
		rec = state.EventNotifyRecord{
			IncidentKey: key, Notifier: target.Name(), Phase: phase,
			Active: active, LastSentAt: now, Subject: msg.Subject, Body: msg.Body,
		}
		if err := n.save(rec); err != nil {
			n.logger.Error("persist event notification state", "notifier", target.Name(), "error", err)
		}
	}
}

func (n *EventNotifier) send(ctx context.Context, target notify.Notifier, e Event, msg notify.Message) bool {
	if err := sendEventNotification(ctx, target, msg); err != nil {
		n.logger.Error("event notification failed", "notifier", target.Name(),
			eventFieldKind, e.Kind, eventFieldService, e.Service,
			eventFieldWatch, e.Watch, eventFieldApp, e.App, "error", err)
		return false
	}
	return true
}

func (n *EventNotifier) remind(ctx context.Context) {
	if n.inPanic != nil && n.inPanic() {
		return
	}
	targets, interval := n.deliveryConfig()
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
			msg := notify.Message{Subject: rec.Subject + " (continues)", Body: rec.Body}
			if err := sendEventNotification(ctx, target, msg); err != nil {
				n.logger.Error("event reminder failed", "notifier", target.Name(), "error", err)
				continue
			}
			rec.LastSentAt = now
			if err := n.save(rec); err != nil {
				n.logger.Error("persist event reminder state", "notifier", target.Name(), "error", err)
			}
		}
	}
}

func sendEventNotification(ctx context.Context, target notify.Notifier, msg notify.Message) error {
	sendCtx, cancel := context.WithTimeout(ctx, eventNotifyTimeout)
	defer cancel()
	if err := target.Send(sendCtx, msg); err != nil {
		return fmt.Errorf("send event notification: %w", err)
	}
	return nil
}

func (n *EventNotifier) load(key, target string) (state.EventNotifyRecord, bool, error) {
	if n.store != nil {
		rec, found, err := n.store.EventNotifyState(key, target)
		if err != nil {
			return state.EventNotifyRecord{}, false, fmt.Errorf("load incident state: %w", err)
		}
		return rec, found, nil
	}
	rec, found := n.memory[key+"\x00"+target]
	return rec, found, nil
}

func (n *EventNotifier) save(rec state.EventNotifyRecord) error {
	if n.store != nil {
		if err := n.store.SetEventNotifyState(rec); err != nil {
			return fmt.Errorf("save incident state: %w", err)
		}
		return nil
	}
	n.memory[rec.IncidentKey+"\x00"+rec.Notifier] = rec
	return nil
}

func (n *EventNotifier) due(target string, before time.Time) ([]state.EventNotifyRecord, error) {
	if n.store != nil {
		records, err := n.store.DueEventNotifyStates(target, before)
		if err != nil {
			return nil, fmt.Errorf("load due reminders: %w", err)
		}
		return records, nil
	}
	var due []state.EventNotifyRecord
	for _, rec := range n.memory {
		if rec.Notifier == target && rec.Active && !rec.LastSentAt.After(before) {
			due = append(due, rec)
		}
	}
	return due, nil
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
	case eventKindError, eventKindHookFail, eventKindExpandFailed, eventKindKillFailed, eventKindMakeStepFailed:
		category = e.Kind
		if e.Action != "" {
			category += ":" + e.Action
		}
	}
	active = category == eventNotifyCategoryHealth && phase != eventNotifyPhaseRecovered
	return formatEventNotifyKey(dimension, name, e.Rule, e.Check, category), active, phase
}

func shouldDeliverEvent(kind string, active bool, phase string, rec state.EventNotifyRecord, found bool, now time.Time, interval time.Duration) bool {
	if kind == eventKindRecovered {
		return found && rec.Active
	}
	if !found || (active && (!rec.Active || rec.Phase != phase)) {
		return true
	}
	if !active {
		if interval <= 0 {
			interval = eventNotifyErrorInterval
		}
		return now.Sub(rec.LastSentAt) >= interval
	}
	return interval > 0 && now.Sub(rec.LastSentAt) >= interval
}

func (n *EventNotifier) message(e Event) notify.Message {
	var subject strings.Builder
	subject.WriteString("[sermo] " + n.host)
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
	return notify.Message{Subject: subject.String(), Body: strings.Join(details, "\n")}
}

func eventNeedsNotification(e Event) bool {
	switch e.Kind {
	case eventKindFiring, eventKindWarning, eventKindRecovered, eventKindAlert,
		eventKindError, eventKindHookFail, eventKindExpandFailed,
		eventKindKillFailed, eventKindMakeStepFailed:
		return true
	case eventKindDryRun, eventKindSuppressed, eventKindAction:
		// A service check claimed by a remediation rule has no standalone firing
		// event, so the rule outcome is its alarm, including in dry-run mode.
		return e.Service != "" && e.Rule != "" && e.Action != ""
	default:
		return false
	}
}
