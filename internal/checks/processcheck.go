package checks

import (
	"context"
	"fmt"
	"time"

	"sermo/internal/process"
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
}

func (c processCheck) Run(_ context.Context) Result {
	start := time.Now()
	if c.observe == nil && c.observeAny == nil {
		return c.result(false, "process discovery unavailable", start)
	}
	state := c.observedState()
	ok := state == c.expect
	return c.result(ok, fmt.Sprintf("state %s (want %s)", state, c.expect), start)
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
