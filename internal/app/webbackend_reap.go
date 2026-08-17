package app

import (
	"context"

	"sermo/internal/process"
	"sermo/internal/web"
)

// reapActionLabel names the action in events and rejection messages.
const reapActionLabel = process.SectionReap

// ReapStrays signals the service's stray processes — the members of its init
// unit's control group that no selector claims and that no longer hang off its
// principal process — through the same operation engine, lock, guards and single
// event as `sermoctl reap --apply`.
//
// Authority is the service's own `reap.kill_only_if` and nothing else: with no such
// block the engine reports every stray and signals none, so a browser cannot widen
// what Sermo may kill. The admin role and the CSRF header are enforced upstream by
// the server's auth middleware.
//
// It deliberately does not go through operationResultWithMonitor: reaping changes
// no unit state, so there is no monitor to pause, no settling phase to open and no
// init status cache to invalidate. That is the CloseSSHSession shape, for the same
// reason — the daemon keeps running while one of its leftovers is cleared.
func (b *WebBackend) ReapStrays(ctx context.Context, name string) web.ActionResult {
	e := b.entries[name]
	if e == nil {
		return b.operateError(name, reapActionLabel, unknownServiceMessage+name)
	}
	if e.disabled {
		return b.operateError(name, reapActionLabel, serviceSubjectPrefix+name+" is disabled in configuration")
	}
	return webActionResultFrom(e.engine.Reap(ctx, true), name, reapActionLabel)
}
