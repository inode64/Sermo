package app

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"sermo/internal/severity"
)

const (
	persistenceCheck          = "state-persistence"
	persistenceReportInterval = time.Minute
)

// A writer is identified by its owner and state slot, independently of how
// notification incidents encode their keys.
type persistenceWriter struct {
	service string
	watch   string
	app     string
	check   string
	source  string
}

// PersistenceEvents owns one daemon incident for internal state write errors.
// Service operation results pass through unchanged, retaining their full audit.
type PersistenceEvents struct {
	mu          sync.Mutex
	failed      map[persistenceWriter]struct{}
	last        time.Time
	now         func() time.Time
	emit        func(Event)
	pending     *Event
	dispatching bool
	restored    bool
}

// NewPersistenceEvents groups failures before logging or notification delivery.
func NewPersistenceEvents(emit func(Event), notifications *EventNotifier) *PersistenceEvents {
	return &PersistenceEvents{failed: make(map[persistenceWriter]struct{}), now: time.Now, emit: emit, restored: notifications.persistenceIncidentOpen()}
}

// Emit closes only the writer that succeeded, not unrelated writer failures.
func (p *PersistenceEvents) Emit(e Event) {
	if e.persistenceSource == "" {
		emitSafe(p.emit, e)
		return
	}
	p.mu.Lock()
	event, changed := p.observe(e)
	p.publishLocked(event, changed)
}

// publishLocked releases the lock before downstream I/O. A busy emitter
// coalesces concurrent state transitions to the latest one, preserving order
// and bounded memory while other workers continue monitoring.
func (p *PersistenceEvents) publishLocked(event Event, changed bool) {
	if !changed {
		p.mu.Unlock()
		return
	}
	if p.dispatching {
		p.pending = &event
		p.mu.Unlock()
		return
	}
	p.dispatching = true
	p.mu.Unlock()
	for {
		emitSafe(p.emit, event)
		p.mu.Lock()
		if p.pending == nil {
			p.dispatching = false
			p.mu.Unlock()
			return
		}
		event = *p.pending
		p.pending = nil
		p.mu.Unlock()
	}
}

func (p *PersistenceEvents) observe(e Event) (Event, bool) {
	key := persistenceWriter{service: e.Service, watch: e.Watch, app: e.App, check: e.Check, source: e.persistenceSource}
	if e.Kind == eventKindPersistence {
		if _, failed := p.failed[key]; !failed && !p.restored {
			return Event{}, false
		}
		p.restored = false
		delete(p.failed, key)
		return p.recovered()
	}
	p.restored = false
	first := len(p.failed) == 0
	p.failed[key] = struct{}{}
	now := p.now()
	if !first && now.Sub(p.last) < persistenceReportInterval {
		return Event{}, false
	}
	p.last = now
	return Event{Kind: eventKindFiring, Check: persistenceCheck, Severity: severity.Error,
		Message: fmt.Sprintf("state persistence unavailable (%d failing writers); %s", len(p.failed), e.Message)}, true
}

func (p *PersistenceEvents) recovered() (Event, bool) {
	if len(p.failed) != 0 {
		return Event{}, false
	}
	p.last = time.Time{}
	return Event{Kind: eventKindRecovered, Check: persistenceCheck, Severity: severity.Error, Message: "no remaining failing state writers"}, true
}

// retain forgets removed writers during a generation change. Otherwise a
// removed watch could leave an incident open that nothing could ever close.
func (p *PersistenceEvents) retain(scope eventNotifyScope) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if len(p.failed) == 0 {
		p.mu.Unlock()
		return
	}
	for key := range p.failed {
		_, service := scope.services[key.service]
		if !service && !scope.watches[key.watch] && !scope.apps[key.app] {
			delete(p.failed, key)
		}
	}
	event, changed := p.recovered()
	p.publishLocked(event, changed)
}

// PersistenceErrorLogger bounds repeated snapshot/event-log failures, which
// have no per-target recovery edges. Counts describe omitted log lines only.
func PersistenceErrorLogger(logger *slog.Logger) func(string, error) {
	var mu sync.Mutex
	var last time.Time
	var suppressed uint64
	return func(operation string, err error) {
		mu.Lock()
		now := time.Now()
		if !last.IsZero() && now.Sub(last) < persistenceReportInterval {
			suppressed++
			mu.Unlock()
			return
		}
		omitted := suppressed
		last, suppressed = now, 0
		mu.Unlock()
		logger.Warn("state persistence failed", "operation", operation, "error", err, "suppressed", omitted)
	}
}

// reportPersistenceResult marks internal writes without confusing their
// acknowledgements with a service/watch health recovery.
func reportPersistenceResult(emit func(Event), e Event, source, action string, err error) {
	e.persistenceSource = source
	e.Kind = eventKindPersistence
	if err != nil {
		e.Kind, e.Message = eventKindError, fmt.Sprintf("%s: %v", action, err)
	}
	emitSafe(emit, e)
}
