package checks

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/process"
	"sermo/internal/severity"
)

const (
	// CheckKeyProcesses reuses the service process-selector vocabulary.
	CheckKeyProcesses = process.SectionProcesses
	// DataKeyTerminalOutput carries only current, typed terminal wait evidence.
	DataKeyTerminalOutput   = "terminal_output"
	terminalOutputInterval  = time.Minute
	terminalOutputStableFor = 5 * time.Minute
	terminalOutputTimeout   = 30 * time.Second
)

// TerminalOutputFunc samples configured roots, optionally constrained to the
// owning service's principals by the daemon.
type TerminalOutputFunc func(context.Context, []process.Selector) ([]process.TerminalOutput, error)

// TerminalOutputConfig is shared by construction and validation; process
// selectors remain owned by process.ParseSelectors.
func TerminalOutputConfig(entry map[string]any) ([]process.Selector, time.Duration, error) {
	selectors, warnings := process.ParseSelectors(map[string]any{process.SectionProcesses: entry[CheckKeyProcesses]})
	if len(warnings) > 0 {
		return nil, 0, fmt.Errorf("terminal_output processes: %s", strings.Join(warnings, "; "))
	}
	if len(selectors) == 0 {
		return nil, 0, errors.New("terminal_output requires processes with exact exe and user")
	}
	for _, s := range selectors {
		if !s.IsStrictPrincipal() {
			return nil, 0, errors.New("terminal_output requires strict, non-delegated process selectors")
		}
	}
	stableFor := terminalOutputStableFor
	if raw, ok := entry[CheckKeyStableFor]; ok {
		stableFor = cfgval.Duration(raw)
		if stableFor <= 0 {
			return nil, 0, errors.New("terminal_output stable_for must be a positive duration")
		}
	}
	return selectors, stableFor, nil
}

type terminalOutputCheck struct {
	base
	selectors []process.Selector
	sample    TerminalOutputFunc
	stableFor time.Duration
	clock     func() time.Time
	mu        sync.Mutex
	last      time.Time
	since     map[process.TerminalOutput]time.Time
	cached    Result
}

func (c *terminalOutputCheck) resultMetadata() Result {
	res := c.base.resultMetadata()
	res.ActionBlocked = true
	return res
}

func buildTerminalOutputCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	selectors, stableFor, err := TerminalOutputConfig(entry)
	if err != nil {
		return nil, err.Error()
	}
	sample := deps.TerminalOutput
	if sample == nil {
		if deps.Service != "" {
			return nil, "terminal_output needs owning service process discovery"
		}
		sample = process.ScanTerminalOutput
	}
	if b.timeout <= 0 {
		b.timeout = terminalOutputTimeout
	}
	if !b.severity.Valid() {
		b.severity = severity.Warning
	}
	return &terminalOutputCheck{base: b, selectors: selectors, sample: sample, stableFor: stableFor, clock: time.Now, since: map[process.TerminalOutput]time.Time{}}, ""
}

func (c *terminalOutputCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	if !c.mu.TryLock() {
		res := c.unavailableResult("terminal output observation already running", run.start)
		res.ActionBlocked = true
		return res
	}
	defer c.mu.Unlock()
	now := c.clock()
	if err := ctx.Err(); err != nil {
		c.since = map[process.TerminalOutput]time.Time{}
		c.last, c.cached = now, c.failedObservation(err, run.start)
		return c.cached
	}
	if !c.last.IsZero() && now.Sub(c.last) < terminalOutputInterval {
		return c.cached
	}
	targets, err := c.sample(ctx, c.selectors)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		c.since = map[process.TerminalOutput]time.Time{}
		c.cached = c.failedObservation(err, run.start)
		c.last = now
		return c.cached
	}
	current := make(map[process.TerminalOutput]time.Time, len(targets))
	var ready []process.TerminalOutput
	for _, target := range targets {
		key := target.Evidence()
		since, ok := c.since[key]
		if !ok {
			since = now
		}
		current[key] = since
		if now.Sub(since) >= c.stableFor {
			ready = append(ready, target)
		}
	}
	c.since = current
	res := c.result(len(targets) == 0, fmt.Sprintf("%d terminal output wait cycle(s), %d stable for %s", len(targets), len(ready), c.stableFor), run.start)
	res.ActionBlocked = len(ready) == 0
	res.Data = maps.Clone(res.Data)
	if res.Data == nil {
		res.Data = map[string]any{}
	}
	res.Data[DataKeyTerminalOutput] = ready
	c.last, c.cached = now, res
	return res
}

func (c *terminalOutputCheck) failedObservation(err error, start time.Time) Result {
	res := c.unavailableResult("terminal output: "+err.Error(), start)
	res.ActionBlocked = true
	return res
}
