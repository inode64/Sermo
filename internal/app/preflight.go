package app

import (
	"context"
	"time"

	"sermo/internal/checks"
	"sermo/internal/web"
)

// preflightDeadline bounds a preflight run generously above a single check's
// timeout so concurrent checks each get their full per-check budget.
func preflightDeadline(perCheck time.Duration) time.Duration {
	if perCheck <= 0 {
		perCheck = 10 * time.Second
	}
	return perCheck + 5*time.Second
}

func preflightToWeb(out checks.Outcome) web.PreflightResult {
	res := web.PreflightResult{OK: out.OK}
	for _, r := range out.Results {
		res.Checks = append(res.Checks, web.Check{
			Name:     r.Check,
			OK:       r.OK,
			Optional: r.Optional,
			Message:  r.Message,
			Ran:      true,
		})
	}
	return res
}

// Preflight runs a service's preflight checks on demand (parity with
// `sermoctl preflight SERVICE`).
func (b *WebBackend) Preflight(ctx context.Context, name string) (web.PreflightResult, bool) {
	e := b.entries[name]
	if e == nil {
		return web.PreflightResult{}, false
	}
	if e.engine.Preflight == nil {
		return web.PreflightResult{OK: true}, true
	}
	deadline := preflightDeadline(b.defaultTimeout)
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	return preflightToWeb(e.engine.Preflight(ctx)), true
}