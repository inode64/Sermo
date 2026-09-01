package app

import "time"

// clockOrNow resolves an injectable clock. Nil means the real wall clock.
func clockOrNow(clock func() time.Time) func() time.Time {
	if clock == nil {
		return time.Now
	}
	return clock
}
