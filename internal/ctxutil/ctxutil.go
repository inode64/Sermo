// Package ctxutil provides small context-aware waiting helpers shared across
// packages.
package ctxutil

import (
	"context"
	"time"
)

// Sleep waits for d or for ctx to be cancelled, reporting false when it was
// cancelled first. The timer is always stopped, so a cancelled Sleep leaks no
// goroutine — unlike time.Sleep, which is not cancellable and would block for
// the full duration.
func Sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
