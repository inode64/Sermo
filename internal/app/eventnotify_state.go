package app

import (
	"fmt"
	"time"

	"sermo/internal/notify"
	"sermo/internal/severity"
	"sermo/internal/state"
)

// Bound the outage overlay without evicting confirmed deliveries: once full,
// new identities wait for persistence instead of being sent without a receipt.
const (
	eventNotifyMemoryLimit              = 4096
	eventNotifyPersistenceRetryInterval = time.Minute
)

func eventNotifyRecord(e eventNotification, target string, msg notify.Message) state.EventNotifyRecord {
	key, active, phase := eventNotifyIdentity(e.Event)
	return state.EventNotifyRecord{
		IncidentKey: key, Notifier: target, Phase: phase, Active: active,
		Subject: msg.Subject, Body: msg.Body, Severity: msg.Severity.String(),
		AlarmEpoch: e.alarmEpoch,
	}
}

func eventNotifyRecordKey(incident, target string) string { return incident + "\x00" + target }

func (n *EventNotifier) canRemember(incident, target string) bool {
	_, known := n.memory[eventNotifyRecordKey(incident, target)]
	if known || len(n.memory) < eventNotifyMemoryLimit {
		return true
	}
	now := n.now()
	if n.capacityLogAt.IsZero() || now.Sub(n.capacityLogAt) >= eventNotifyLogInterval {
		n.capacityLogAt = now
		n.logger.Error("event notification receipt capacity reached; new deliveries skipped until persistence recovers", "pending", len(n.memory))
	}
	return false
}

func (n *EventNotifier) load(key, target string) (state.EventNotifyRecord, bool, error) {
	if rec, found := n.memory[eventNotifyRecordKey(key, target)]; found {
		return rec, true, nil
	}
	if n.store == nil {
		return state.EventNotifyRecord{}, false, nil
	}
	rec, found, err := n.store.EventNotifyState(key, target)
	if err != nil {
		return state.EventNotifyRecord{}, false, fmt.Errorf("load incident state: %w", err)
	}
	return normalizeLegacyNotifyRecord(rec), found, nil
}

// save remembers a confirmed delivery BEFORE attempting persistence. All callers
// hold deliveryMu. The overlay wins over stale SQLite rows, including closed
// episodes and reminder timestamps, until the write is confirmed.
func (n *EventNotifier) save(rec state.EventNotifyRecord) {
	key := eventNotifyRecordKey(rec.IncidentKey, rec.Notifier)
	n.memory[key] = rec
	if n.store == nil || n.now().Before(n.persistRetryAt) {
		return
	}
	n.persistReceipt(rec)
}

// persistReceipt removes an overlay entry only after the store confirms it.
// Its callers hold deliveryMu and check the retry deadline before writing.
func (n *EventNotifier) persistReceipt(rec state.EventNotifyRecord) bool {
	if err := n.store.SetEventNotifyState(rec); err != nil {
		n.persistFailed = true
		n.persistRetryAt = n.now().Add(eventNotifyPersistenceRetryInterval)
		n.logger.Error("event notification persistence unavailable; keeping delivery receipts in memory", "error", err, "pending", len(n.memory))
		return false
	}
	delete(n.memory, eventNotifyRecordKey(rec.IncidentKey, rec.Notifier))
	return true
}

// retryPersistence runs even without reminders configured and during panic:
// writing receipts is not notification delivery. Stop at the first failure so
// an outage costs one write attempt per scan, not one per incident.
func (n *EventNotifier) retryPersistence() {
	if n.store == nil || n.now().Before(n.persistRetryAt) {
		return
	}
	for _, rec := range n.memory {
		if !n.persistReceipt(rec) {
			return
		}
	}
	if n.persistFailed {
		n.persistFailed = false
		n.logger.Info("event notification persistence recovered")
	}
}

func (n *EventNotifier) due(target string, before time.Time) ([]state.EventNotifyRecord, error) {
	var records []state.EventNotifyRecord
	if n.store != nil {
		stored, err := n.store.DueEventNotifyStates(target, before)
		if err != nil {
			return nil, fmt.Errorf("load due reminders: %w", err)
		}
		records = stored
	}
	// A newer in-memory recovery/reminder must hide an old due database row.
	due := make([]state.EventNotifyRecord, 0, len(records)+len(n.memory))
	for _, rec := range records {
		if n.scope != nil && !n.scope.live(rec.IncidentKey) {
			continue
		}
		if _, overridden := n.memory[eventNotifyRecordKey(rec.IncidentKey, target)]; !overridden {
			due = append(due, normalizeLegacyNotifyRecord(rec))
		}
	}
	for _, rec := range n.memory {
		if rec.Notifier == target && rec.Active && !rec.LastSentAt.After(before) {
			due = append(due, rec)
		}
	}
	return due, nil
}

// serviceIncidents is one service's open health incidents its worker can close:
// those of its checks and of its rules, each at the gravest grade a configured
// notifier holds it.
type serviceIncidents struct {
	checks map[string]severity.Level
	rules  map[string]severity.Level
}

// openServiceIncidents lists, per service, the open check and rule incidents a
// new generation's workers adopt before their first cycle (see
// Worker.restoreIncidents). A check snapshot or rule window from another
// configuration is not restored, so the worker started with the episode closed
// and never sent the recovery: event_notify reminded the incident every
// repeat_interval although the service was healthy.
func (n *EventNotifier) openServiceIncidents() map[string]serviceIncidents {
	if n == nil {
		return nil
	}
	n.deliveryMu.Lock()
	defer n.deliveryMu.Unlock()
	keys := map[string]bool{}
	if n.store != nil {
		stored, err := n.store.ActiveEventNotifyIncidents()
		if err != nil {
			n.logger.Error("list open event notifications to restore", "error", err)
		}
		for _, key := range stored {
			keys[key] = true
		}
	}
	for _, rec := range n.memory {
		if rec.Active {
			keys[rec.IncidentKey] = true
		}
	}
	targets, _ := n.deliveryConfig()
	open := map[string]serviceIncidents{}
	for key := range keys {
		parts, ok := parseEventNotifyKey(key)
		if !ok || parts[0] != eventNotifyDimensionService || parts[4] != eventNotifyCategoryHealth {
			continue
		}
		service, rule, check := parts[1], parts[2], parts[3]
		if (rule == "") == (check == "") {
			continue
		}
		held, active := n.heldIncident(key, targets)
		if !active {
			continue
		}
		inc := open[service]
		if rule != "" {
			inc.rules = addHeld(inc.rules, rule, held)
		} else {
			inc.checks = addHeld(inc.checks, check, held)
		}
		open[service] = inc
	}
	return open
}

// heldIncident reports whether a configured notifier still holds key open, and
// the gravest grade any of them heard. A notifier removed from the
// configuration is not reminded, so its rows do not keep an incident open.
// Callers hold deliveryMu.
func (n *EventNotifier) heldIncident(key string, targets []notify.Notifier) (severity.Level, bool) {
	var held severity.Level
	active := false
	for _, target := range targets {
		rec, found, err := n.load(key, target.Name())
		if err != nil {
			n.logger.Error("load open event notification to restore", "notifier", target.Name(), "error", err)
			continue
		}
		if found && rec.Active {
			active = true
			held = severity.Max(held, severity.Level(rec.Severity).Resolved())
		}
	}
	return held, active
}

func addHeld(held map[string]severity.Level, name string, level severity.Level) map[string]severity.Level {
	if held == nil {
		held = map[string]severity.Level{}
	}
	held[name] = level
	return held
}

// persistenceIncidentOpen reconciles the daemon-wide persistence episode on
// restart. A successful state write can close an incident recorded by the old
// process even though its per-writer failure set lived only in memory.
func (n *EventNotifier) persistenceIncidentOpen() bool {
	if n == nil {
		return false
	}
	n.deliveryMu.Lock()
	defer n.deliveryMu.Unlock()
	targets, _ := n.deliveryConfig()
	key, _, _ := eventNotifyIdentity(Event{Check: persistenceCheck, Kind: eventKindFiring})
	for _, target := range targets {
		rec, found, err := n.load(key, target.Name())
		if err != nil {
			n.logger.Error("restore persistence incident", "error", err)
			return true
		}
		if found && rec.Active {
			return true
		}
	}
	return false
}
