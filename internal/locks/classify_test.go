package locks

import (
	"testing"
	"time"
)

// classify decides whether a lock may be reclaimed; stomping a live lock is a
// safety violation, so pin the precedence (TTL first) and the conservative
// defaults (unreadable start-ticks is NOT treated as PID reuse).
func TestClassify(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	live := fakeProc{alive: map[int]bool{100: true}, ticks: map[int]uint64{100: 884512}}
	cases := []struct {
		name  string
		lf    lockFile
		proc  ProcessProber
		state State
	}{
		{"live owner, ticks match, TTL ahead -> active",
			lockFile{OwnerPID: 100, OwnerStartTicks: 884512, ExpiresAt: now.Add(time.Hour)}, live, StateActive},
		{"TTL elapsed even with a live owner -> expired",
			lockFile{OwnerPID: 100, OwnerStartTicks: 884512, ExpiresAt: now.Add(-time.Second)}, live, StateExpired},
		{"dead owner -> stale",
			lockFile{OwnerPID: 200, ExpiresAt: now.Add(time.Hour)}, fakeProc{alive: map[int]bool{200: false}}, StateStale},
		{"live owner but start-ticks differ (PID reuse) -> stale",
			lockFile{OwnerPID: 100, OwnerStartTicks: 1, ExpiresAt: now.Add(time.Hour)}, live, StateStale},
		{"no TTL + live owner -> active",
			lockFile{OwnerPID: 100, OwnerStartTicks: 884512}, live, StateActive},
		{"unreadable start-ticks must not read as reuse -> active",
			lockFile{OwnerPID: 100, OwnerStartTicks: 884512}, fakeProc{alive: map[int]bool{100: true}}, StateActive},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, _ := classify(c.lf, now, c.proc); got != c.state {
				t.Fatalf("classify = %q, want %q", got, c.state)
			}
		})
	}
}
