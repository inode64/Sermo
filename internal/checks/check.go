// Package checks runs a service's monitoring/preflight/postflight checks
// (sections 12 and 19). Each check is single-shot and stateless; the runner
// executes a set concurrently and returns one Result per check.
//
// MVP check types: tcp, http, command, service, file_exists, binary. The
// process and metric types (the latter needs the daemon's stateful collector)
// and the libraries preflight type are not yet implemented.
package checks

import (
	"context"
	"sync"
	"time"
)

// Result is the observable outcome of one check (section 12).
type Result struct {
	Service  string         `json:"service,omitempty"`
	Check    string         `json:"check"`
	OK       bool           `json:"ok"`
	Optional bool           `json:"optional,omitempty"`
	Skipped  bool           `json:"skipped,omitempty"` // gated off this cycle (requires/skip_when_changed)
	Message  string         `json:"message,omitempty"`
	Latency  time.Duration  `json:"latency_ns,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
}

// Check is a single-shot probe.
type Check interface {
	Name() string
	Run(ctx context.Context) Result
}

// Built pairs a check with whether its failure is optional (a warning) or
// required (blocks the action), per section 19.
type Built struct {
	Check    Check
	Optional bool
}

// Run executes checks concurrently and returns their results in input order.
// maxParallel bounds concurrency; 0 means unbounded (the sermoctl one-shot
// path; the daemon's global semaphore is a separate concern, section 12).
func Run(ctx context.Context, built []Built, maxParallel int) []Result {
	results := make([]Result, len(built))
	var sem chan struct{}
	if maxParallel > 0 {
		sem = make(chan struct{}, maxParallel)
	}

	var wg sync.WaitGroup
	for i, b := range built {
		wg.Add(1)
		go func(i int, b Built) {
			defer wg.Done()
			if sem != nil {
				sem <- struct{}{}
				defer func() { <-sem }()
			}
			res := b.Check.Run(ctx)
			res.Optional = b.Optional
			results[i] = res
		}(i, b)
	}
	wg.Wait()
	return results
}

// base carries the fields every check shares and applies the per-check timeout.
type base struct {
	name    string
	service string
	timeout time.Duration
}

func (b base) Name() string { return b.name }

// withTimeout derives the check's deadline from the caller's context.
func (b base) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if b.timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, b.timeout)
}

func (b base) result(ok bool, message string, start time.Time) Result {
	return Result{
		Service: b.service,
		Check:   b.name,
		OK:      ok,
		Message: message,
		Latency: time.Since(start),
	}
}
