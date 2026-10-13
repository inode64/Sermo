package app

import (
	"fmt"
	"time"

	"sermo/internal/notify"
	"sermo/internal/state"
)

// Bound the outage overlay without evicting confirmed deliveries: once full,
// new identities wait for persistence instead of being sent without a receipt.
const (
	eventNotifyMemoryLimit              = 4096
	eventNotifyPersistenceRetryInterval = time.Minute
)

func eventNotifyRecord(e Event, target string, msg notify.Message) state.EventNotifyRecord {
	key, active, phase := eventNotifyIdentity(e)
	return state.EventNotifyRecord{
		IncidentKey: key, Notifier: target, Phase: phase, Active: active,
		Subject: msg.Subject, Body: msg.Body, Severity: msg.Severity.String(),
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
