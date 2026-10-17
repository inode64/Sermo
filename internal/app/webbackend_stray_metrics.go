package app

import (
	"time"

	"sermo/internal/utmp"
	"sermo/internal/web"
)

// Cache terminal lookups within one inventory, including unavailable devices.
func cachedTerminalAccess(read func(uint64) (time.Time, bool)) func(uint64) (time.Time, bool) {
	if read == nil {
		read = func(device uint64) (time.Time, bool) {
			return utmp.TerminalAccessedAt(utmp.DevRoot, device)
		}
	}
	type access struct {
		at time.Time
		ok bool
	}
	cache := make(map[uint64]access)
	return func(device uint64) (time.Time, bool) {
		if result, found := cache[device]; found {
			return result.at, result.ok
		}
		at, ok := read(device)
		cache[device] = access{at: at, ok: ok}
		return at, ok
	}
}

func (b *WebBackend) attachStrayMetrics(inventory *web.SessionInventory, snapshot sessionProcessSnapshot, active map[string]struct{}, accessedAt func(uint64) (time.Time, bool)) {
	for i := range inventory.Strays {
		row := &inventory.Strays[i]
		identity, ok := snapshot.byPID[row.PID]
		if !ok || !identity.StartTicksOK || row.StartTicks == 0 || identity.StartTicks != row.StartTicks {
			continue
		}
		// Usage belongs to this process, not its descendants. Include the process
		// generation in the key so a reused PID cannot inherit rate baselines.
		key := sessionMetricKey("stray", row.Service, row.Identity)
		active[key] = struct{}{}
		if b.sessionMetricCollector != nil {
			row.SessionUsage = sessionUsage(b.sessionMetricCollector.SampleService(key, []int{row.PID}))
		}
		// A multiplexer server may have no controlling terminal itself. Its
		// descendants can still supply terminal activity, independently of exe.
		idle := web.TerminalSession{}
		attachTerminalSessionIdle(snapshot, &idle, snapshot.treePIDs([]int{row.PID}), b.webNow(), accessedAt)
		row.IdleSeconds, row.HasIdle = idle.IdleSeconds, idle.HasIdle
	}
}
