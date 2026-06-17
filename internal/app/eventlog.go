package app

import (
	"sync"
	"time"

	"sermo/internal/state"
)

// LoggedEvent is an Event with the time it was recorded.
type LoggedEvent struct {
	Time time.Time
	Event
}

// EventStore persists the operator-visible event/activity feed so sermod can
// repopulate the web UI after a daemon restart.
type EventStore interface {
	RecordEvent(state.EventRecord) error
	RecentEvents(limit int) ([]state.EventRecord, error)
	PruneEvents(before time.Time) (int64, error)
}

// EventLog keeps the most recent events in a bounded ring buffer so the web UI
// can show a global feed and a per-service feed quickly. When an EventStore is
// attached, the ring is hydrated from persistent state at startup and every new
// event is appended to the store. It is safe for concurrent use; workers and
// watches add, the web reads.
type EventLog struct {
	mu            sync.Mutex
	now           func() time.Time
	store         EventStore
	onStoreError  func(error)
	buf           []LoggedEvent
	size          int
	next          int // write index
	count         int
	lastByService map[string]LoggedEvent
	lastByWatch   map[string]LoggedEvent
}

// NewEventLog returns a log retaining the last size events (min 1).
func NewEventLog(size int) *EventLog {
	if size < 1 {
		size = 1
	}
	return &EventLog{
		now:           time.Now,
		size:          size,
		buf:           make([]LoggedEvent, size),
		lastByService: map[string]LoggedEvent{},
		lastByWatch:   map[string]LoggedEvent{},
	}
}

// NewPersistentEventLog returns an EventLog backed by store. It loads the last
// retained events into the in-memory ring; if that hydration fails, the returned
// log is still usable and remains attached for future writes.
func NewPersistentEventLog(size int, store EventStore, onStoreError func(error)) (*EventLog, error) {
	l := NewEventLog(size)
	l.store = store
	l.onStoreError = onStoreError
	if store == nil {
		return l, nil
	}
	if err := l.loadRecentFromStore(); err != nil {
		return l, err
	}
	return l, nil
}

// Add records an event with the current time, evicting the oldest when full.
func (l *EventLog) Add(e Event) {
	if l == nil {
		return
	}
	now := l.now
	if now == nil {
		now = time.Now
	}
	logged := LoggedEvent{Time: now(), Event: e}
	l.mu.Lock()
	l.addLocked(logged)
	l.mu.Unlock()

	if l.store != nil {
		if err := l.store.RecordEvent(eventRecordFromLogged(logged)); err != nil {
			l.reportStoreError(err)
		}
	}
}

// Recent returns up to limit events, newest first. A non-empty service filters to
// that service's events (Event.Service); "" returns everything (including
// host-watch events). limit <= 0 returns all retained events.
func (l *EventLog) Recent(service string, limit int) []LoggedEvent {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	ordered := l.orderedLocked() // oldest..newest
	l.mu.Unlock()

	// Size from the snapshot, not l.count: l.count is mutated by Add under the
	// lock we just released, so reading it here would be a data race.
	out := make([]LoggedEvent, 0, len(ordered))
	for i := len(ordered) - 1; i >= 0; i-- {
		if limit > 0 && len(out) >= limit {
			break
		}
		if service != "" && ordered[i].Service != service {
			continue
		}
		out = append(out, ordered[i])
	}
	return out
}

// LastService returns the newest retained event for service, if any.
func (l *EventLog) LastService(service string) (LoggedEvent, bool) {
	if l == nil || service == "" {
		return LoggedEvent{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	ev, ok := l.lastByService[service]
	return ev, ok
}

// LastWatchActivity returns the newest retained watch-activity event for watch.
func (l *EventLog) LastWatchActivity(watch string) (LoggedEvent, bool) {
	if l == nil || watch == "" {
		return LoggedEvent{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	ev, ok := l.lastByWatch[watch]
	return ev, ok
}

func (l *EventLog) orderedLocked() []LoggedEvent {
	out := make([]LoggedEvent, 0, l.count)
	if l.count < l.size {
		out = append(out, l.buf[:l.count]...)
		return out
	}
	out = append(out, l.buf[l.next:]...)
	out = append(out, l.buf[:l.next]...)
	return out
}

func (l *EventLog) addLocked(e LoggedEvent) {
	l.buf[l.next] = e
	l.next = (l.next + 1) % l.size
	if l.count < l.size {
		l.count++
	}
	l.indexLocked(e)
}

func (l *EventLog) indexLocked(e LoggedEvent) {
	if e.Service != "" {
		if l.lastByService == nil {
			l.lastByService = map[string]LoggedEvent{}
		}
		l.lastByService[e.Service] = e
	}
	if e.Watch != "" && isWatchActivityKind(e.Kind) {
		if l.lastByWatch == nil {
			l.lastByWatch = map[string]LoggedEvent{}
		}
		l.lastByWatch[e.Watch] = e
	}
}

func (l *EventLog) rebuildIndexesLocked() {
	l.lastByService = map[string]LoggedEvent{}
	l.lastByWatch = map[string]LoggedEvent{}
	for _, e := range l.orderedLocked() {
		l.indexLocked(e)
	}
}

func (l *EventLog) loadRecentFromStore() error {
	records, err := l.store.RecentEvents(l.size)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = make([]LoggedEvent, l.size)
	l.next = 0
	l.count = 0
	for i := len(records) - 1; i >= 0; i-- {
		l.addLocked(loggedEventFromRecord(records[i]))
	}
	l.rebuildIndexesLocked()
	return nil
}

// Prune removes events strictly older than 'before'. If before.IsZero(), all
// events are cleared. Returns the number of events removed. Safe for concurrent use.
func (l *EventLog) Prune(before time.Time) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()

	if l.count == 0 {
		l.mu.Unlock()
		return l.pruneStore(before, 0)
	}
	var cleared int
	if before.IsZero() {
		cleared = l.count
		l.buf = make([]LoggedEvent, l.size)
		l.next = 0
		l.count = 0
		l.rebuildIndexesLocked()
		l.mu.Unlock()
		return l.pruneStore(before, cleared)
	}

	ordered := l.orderedLocked() // oldest first
	keepIdx := 0
	for i := range ordered {
		if !ordered[i].Time.Before(before) {
			keepIdx = i
			break
		}
		keepIdx = i + 1
	}
	kept := ordered[keepIdx:]
	cleared = len(ordered) - len(kept)

	// Rebuild the ring with kept events (oldest at [0]).
	newBuf := make([]LoggedEvent, l.size)
	for i, e := range kept {
		if i < l.size {
			newBuf[i] = e
		}
	}
	l.buf = newBuf
	l.count = len(kept)
	if l.count < l.size {
		l.next = l.count
	} else {
		l.next = 0
	}
	l.rebuildIndexesLocked()
	l.mu.Unlock()
	return l.pruneStore(before, cleared)
}

func (l *EventLog) pruneStore(before time.Time, memoryCleared int) int {
	if l.store == nil {
		return memoryCleared
	}
	cleared, err := l.store.PruneEvents(before)
	if err != nil {
		l.reportStoreError(err)
		return memoryCleared
	}
	maxInt := int64(int(^uint(0) >> 1))
	if cleared > maxInt {
		return int(maxInt)
	}
	return int(cleared)
}

func (l *EventLog) reportStoreError(err error) {
	if err != nil && l.onStoreError != nil {
		l.onStoreError(err)
	}
}

// MultiEmit fans an event out to several emitters (e.g. slog plus the event log),
// skipping nil ones.
func MultiEmit(emitters ...func(Event)) func(Event) {
	return func(e Event) {
		for _, emit := range emitters {
			if emit != nil {
				emit(e)
			}
		}
	}
}

func eventRecordFromLogged(e LoggedEvent) state.EventRecord {
	return state.EventRecord{
		At:      e.Time,
		Service: e.Service,
		Watch:   e.Watch,
		Kind:    e.Kind,
		Rule:    e.Rule,
		Action:  e.Action,
		Status:  e.Status,
		Message: e.Message,
	}
}

func loggedEventFromRecord(e state.EventRecord) LoggedEvent {
	return LoggedEvent{
		Time: e.At,
		Event: Event{
			Service: e.Service,
			Watch:   e.Watch,
			Kind:    e.Kind,
			Rule:    e.Rule,
			Action:  e.Action,
			Status:  e.Status,
			Message: e.Message,
		},
	}
}
