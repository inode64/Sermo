package checks

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"sermo/internal/process"
	"sermo/internal/strutil"
)

// processCheck passes when the observed state of processes matching its
// exe/user selector equals the expected state. Matching uses the
// exact resolved-exe and real-UID rules .
type processCheck struct {
	base
	exes       []string
	user       string
	expect     string
	observe    func(exe, user string) string
	observeAny func(exes []string, user string) string
	// stale reports the service's processes whose binary was replaced on disk.
	// It exists to explain an "absent" reading rather than to change it.
	stale StaleBinariesFunc
}

func (c processCheck) Run(_ context.Context) Result {
	start := time.Now()
	if c.observe == nil && c.observeAny == nil {
		return c.unavailableResult("process discovery unavailable", start)
	}
	state := c.observedState()
	ok := state == c.expect
	message := fmt.Sprintf("state %s (want %s)", state, c.expect)
	// A process whose executable was replaced on disk resolves no exe, so an
	// exact-exe selector stops matching it and the service merely *looks* like
	// it has no process. The stale list only names processes that are still
	// running, so the daemon is in fact present — on the previous version. That
	// is the condition the stale-binary check reports, and it gets the same
	// treatment: a verdictless state reading that says why, keeps rules keyed on
	// this check firing, and does not book an outage in health or SLA accounting
	// for a service that is serving. A bare failure read as a dead daemon and
	// sent the operator after a process that was there all along.
	if !ok && state == process.StateAbsent {
		if replaced := c.replacedBinaries(); replaced != "" {
			message += fmt.Sprintf("; %s was replaced on disk, so no process matches this executable; %s",
				replaced, StaleBinaryRestartHint)
			res := c.result(ok, message, start)
			res.Reports = ReportsState
			res.Data = map[string]any{DataKeyReplacedBinaries: replaced}
			return res
		}
	}
	return c.result(ok, message, start)
}

// replacedBinaries names this service's replaced executables that one of this
// check's selectors would have matched, or "" when none apply.
func (c processCheck) replacedBinaries() string {
	if c.stale == nil {
		return ""
	}
	var matched []string
	for _, s := range c.stale() {
		if slices.Contains(c.exes, s.Path) {
			matched = append(matched, s.Path)
		}
	}
	if len(matched) == 0 {
		return ""
	}
	return strings.Join(strutil.Unique(matched), ", ")
}

func (c processCheck) observedState() string {
	if c.observeAny != nil {
		return c.observeAny(c.exes, c.user)
	}
	matchedZombie := false
	for _, exe := range c.exes {
		switch c.observe(exe, c.user) {
		case process.StateRunning:
			return process.StateRunning
		case process.StateZombie:
			matchedZombie = true
		}
	}
	if matchedZombie {
		return process.StateZombie
	}
	return process.StateAbsent
}
