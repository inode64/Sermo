package app

import (
	"sync"
	"time"
)

// LoggedEvent is an Event with the time it was recorded.
type LoggedEvent struct {
	Time time.Time
	Event
}

// EventLog keeps the most recent events in a bounded ring buffer so the web UI
// can show a global feed and a per-service feed without a database. It is safe
// for concurrent use; workers and watches add, the web reads.
type EventLog struct {
	mu    sync.Mutex
	now   func() time.Time
	buf   []LoggedEvent
	size  int
	next  int // write index
	count int
}

// NewEventLog returns a log retaining the last size events (min 1).
func NewEventLog(size int) *EventLog {
	if size < 1 {
		size = 1
	}
	return &EventLog{now: time.Now, size: size, buf: make([]LoggedEvent, size)}
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
	l.mu.Lock()
	l.buf[l.next] = LoggedEvent{Time: now(), Event: e}
	l.next = (l.next + 1) % l.size
	if l.count < l.size {
		l.count++
	}
	l.mu.Unlock()
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

// Prune removes events strictly older than 'before'. If before.IsZero(), all
// events are cleared. Returns the number of events removed. Safe for concurrent use.
func (l *EventLog) Prune(before time.Time) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.count == 0 {
		return 0
	}
	if before.IsZero() {
		cleared := l.count
		l.buf = make([]LoggedEvent, l.size)
		l.next = 0
		l.count = 0
		return cleared
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
	cleared := len(ordered) - len(kept)

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
	return cleared
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
