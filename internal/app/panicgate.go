package app

import (
	"sync"
	"time"

	"sermo/internal/state"
)

const defaultPanicGateTTL = time.Second

// panicReader is the persisted source of the daemon-wide panic flag. Satisfied
// by *state.Store; kept narrow so the gate can be tested without a database.
type panicReader interface {
	Panic() (state.GlobalRecord, bool, error)
}

// PanicGate exposes the global panic-mode flag to the hot monitoring path. While
// panic mode is on the daemon keeps running checks (so status stays visible) but
// suppresses hooks, alert notifications and automatic remediation. Every worker
// and watch checks the gate each cycle, so reads go through a short TTL cache to
// avoid hammering the state database; a read error keeps the last known value so
// the daemon never flaps. The zero/nil gate reports "not in panic".
type PanicGate struct {
	store panicReader
	ttl   time.Duration
	now   func() time.Time

	mu     sync.Mutex
	cached bool
	at     time.Time
	read   bool
}

// NewPanicGate returns a gate backed by store. A nil store means panic mode is
// never on (no persistence).
func NewPanicGate(store panicReader) *PanicGate {
	return &PanicGate{store: store, ttl: defaultPanicGateTTL, now: time.Now}
}

// Active reports whether panic mode is currently on, refreshing from the store
// at most once per ttl. It is safe for concurrent use and nil-safe.
func (g *PanicGate) Active() bool {
	if g == nil || g.store == nil {
		return false
	}
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.read && now.Sub(g.at) < g.ttl {
		return g.cached
	}
	rec, found, err := g.store.Panic()
	if err != nil {
		// Keep the last known state on error rather than flipping the daemon's
		// behavior because of a transient read failure.
		return g.cached
	}
	g.cached = found && rec.On
	g.at = now
	g.read = true
	return g.cached
}
