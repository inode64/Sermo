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

	out := make([]LoggedEvent, 0, l.count)
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
