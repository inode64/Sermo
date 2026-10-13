package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"sermo/internal/notify"
	"sermo/internal/severity"
	"sermo/internal/state"
)

const (
	eventNotifyBurstInterval      = time.Minute
	eventNotifyBurstLimit         = 20
	eventNotifyPriorityLimit      = 5
	eventNotifyPendingLimit       = 256
	eventNotifySummaryLimit       = 20
	eventNotifySummaryDetailLimit = 160
)

// One budget per host/channel, shared by services, watches, apps and reminders.
// Pending delivery is NOT a receipt; only a confirmed summary advances state.
type eventNotifyBurst struct {
	started   time.Time
	ordinary  int
	priority  int
	summaryAt time.Time
	pending   map[string]eventNotifyPending
	overflow  uint64
}

type eventNotifyPending struct {
	record      state.EventNotifyRecord
	message     notify.Message
	notice      bool
	occurrences uint64
}

func (n *EventNotifier) burst(target string) *eventNotifyBurst {
	b := n.bursts[target]
	if b == nil {
		b = &eventNotifyBurst{pending: make(map[string]eventNotifyPending)}
		n.bursts[target] = b
	}
	now := n.now()
	if b.started.IsZero() || now.Sub(b.started) >= eventNotifyBurstInterval {
		b.started, b.ordinary, b.priority = now, 0, 0
	}
	return b
}

func (n *EventNotifier) dispatch(ctx context.Context, target notify.Notifier, e Event, msg notify.Message, priority bool) bool {
	rec := eventNotifyRecord(e, target.Name(), msg)
	return n.dispatchPending(ctx, target, eventNotifyPending{record: rec, message: msg, notice: e.Notice}, priority)
}

func (n *EventNotifier) dispatchReminder(ctx context.Context, target notify.Notifier, rec state.EventNotifyRecord, msg notify.Message) bool {
	// A due database row may predate a queued recovery or escalation. Never
	// replace that newer transition with a stale reminder after a failed summary.
	if b := n.bursts[target.Name()]; b != nil {
		if _, pending := b.pending[rec.IncidentKey]; pending {
			return false
		}
	}
	return n.dispatchPending(ctx, target, eventNotifyPending{record: rec, message: msg}, false)
}

func (n *EventNotifier) dispatchPending(ctx context.Context, target notify.Notifier, pending eventNotifyPending, priority bool) bool {
	b := n.burst(target.Name())
	key := pending.record.IncidentKey
	prev, found := b.pending[key]
	pending.occurrences = 1 + prev.occurrences
	// A fresh budget must not let a lower-grade sample erase an escalation
	// that still awaits delivery in the summary queue.
	if found && pending.record.Active && prev.record.Active && prev.message.Severity.Rank() > pending.message.Severity.Rank() {
		prev.occurrences = pending.occurrences
		b.pending[key] = prev
		return false
	}
	allowed := false
	switch {
	case b.ordinary < eventNotifyBurstLimit:
		b.ordinary++
		allowed = true
	case priority && b.priority < eventNotifyPriorityLimit:
		b.priority++
		allowed = true
	}
	if allowed {
		if !n.send(ctx, target, pending.message) {
			return false
		}
		delete(b.pending, pending.record.IncidentKey)
		return true
	}
	if !found && len(b.pending) >= eventNotifyPendingLimit {
		b.overflow++
		return false
	}
	b.pending[key] = pending
	return false
}

// A deferred opening that clears before delivery needs no orphan recovery.
// Conversely a new failure cancels a deferred recovery for the same episode.
func (n *EventNotifier) reconcilePending(target, key string, e Event, rec state.EventNotifyRecord, found bool) {
	b := n.bursts[target]
	if b == nil {
		return
	}
	pending, exists := b.pending[key]
	if !exists {
		return
	}
	if (e.Kind == eventKindRecovered && (!found || !rec.Active)) ||
		(e.Kind != eventKindRecovered && pending.record.Phase == eventNotifyPhaseRecovered) {
		delete(b.pending, key)
	}
}

func (n *EventNotifier) flushSummaries(ctx context.Context, targets []notify.Notifier) {
	for _, target := range targets {
		b := n.burst(target.Name())
		if len(b.pending) == 0 || n.now().Sub(b.summaryAt) < eventNotifyBurstInterval {
			continue
		}
		b.summaryAt = n.now() // failed transports must not retry on each new event
		keys := n.summaryKeys(b, target)
		if len(keys) == 0 {
			continue
		}
		msg := n.summaryMessage(b, keys)
		if !n.send(ctx, target, msg) {
			continue
		}
		for _, key := range keys {
			pending := b.pending[key]
			if !pending.notice {
				pending.record.LastSentAt = n.now()
				n.save(pending.record)
			}
			delete(b.pending, key)
		}
		b.overflow = 0
	}
}

func (n *EventNotifier) summaryKeys(b *eventNotifyBurst, target notify.Notifier) []string {
	keys := make([]string, 0, len(b.pending))
	for key, pending := range b.pending {
		// A recovery belongs to the channels that heard the incident, even if a
		// reload raised their severity filter in the meantime.
		if pending.record.Phase != eventNotifyPhaseRecovered && !notify.Accepts(target, pending.message.Severity) {
			delete(b.pending, key)
			continue
		}
		if pending.notice || n.canRemember(key, target.Name()) {
			keys = append(keys, key)
		}
	}
	slices.SortFunc(keys, func(a, c string) int {
		rank := b.pending[c].message.Severity.Rank() - b.pending[a].message.Severity.Rank()
		if rank != 0 {
			return rank
		}
		return strings.Compare(a, c)
	})
	capacity := eventNotifyMemoryLimit - len(n.memory)
	selected := keys[:0]
	for _, key := range keys {
		pending := b.pending[key]
		_, known := n.memory[eventNotifyRecordKey(key, target.Name())]
		if !known && !pending.notice {
			if capacity == 0 {
				continue
			}
			capacity--
		}
		selected = append(selected, key)
		if len(selected) == eventNotifySummaryLimit {
			break
		}
	}
	return selected
}

func (n *EventNotifier) summaryMessage(b *eventNotifyBurst, keys []string) notify.Message {
	level := severity.Debug
	var body strings.Builder
	fmt.Fprintf(&body, "%d grouped notifications; %d still pending; %d additional deliveries omitted at queue capacity.\n", len(keys), len(b.pending)-len(keys), b.overflow)
	for _, key := range keys {
		pending := b.pending[key]
		level = severity.Max(level, pending.message.Severity)
		detail := []rune(strings.ReplaceAll(pending.message.Body, "\n", "; "))
		if len(detail) > eventNotifySummaryDetailLimit {
			detail = append(detail[:eventNotifySummaryDetailLimit], '…')
		}
		fmt.Fprintf(&body, "\n%s (%d occurrences) — %s\n", pending.message.Subject, pending.occurrences, string(detail))
	}
	return notify.Message{
		Subject: subjectPrefix(level) + " " + n.host + ": notification summary",
		Body:    body.String(), Severity: level,
	}
}
