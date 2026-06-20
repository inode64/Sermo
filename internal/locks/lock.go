// Package locks reads and classifies Sermo named runtime locks under
// <paths.runtime>/locks. It reports locks for `sermoctl locks
// SERVICE` and backs `sermoctl lock` acquire/release/wrap operations.
//
// The internal operation lock under <paths.runtime>/ops is a separate namespace
// and is never reported here.
package locks

import (
	"fmt"
	"strings"
	"time"
)

// State is whether a lock currently blocks actions.
type State string

const (
	// StateActive blocks the actions its guards cover.
	StateActive State = "active"
	// StateExpired means the TTL elapsed; the lock is inactive and reclaimable.
	StateExpired State = "expired"
	// StateStale means the owner is gone (dead PID or PID reuse); inactive and
	// reclaimable.
	StateStale State = "stale"
)

// Lock is a named runtime lock found on disk, with its computed state.
type Lock struct {
	Service         string    `json:"service"`
	Name            string    `json:"name,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	OwnerPID        int       `json:"owner_pid"`
	OwnerStartTicks uint64    `json:"owner_start_ticks"`
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	Path            string    `json:"path"`
	State           State     `json:"state"`
	StaleReason     string    `json:"stale_reason,omitempty"`
}

// Active reports whether the lock currently blocks actions.
func (l Lock) Active() bool { return l.State == StateActive }

func validateIdentifier(kind, value string, allowEmpty bool) error {
	if value == "" {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("%s must not be empty", kind)
	}
	if value == "." || value == ".." ||
		strings.Contains(value, "/") ||
		strings.Contains(value, `\`) {
		return fmt.Errorf("%s %q must be a simple name without path separators", kind, value)
	}
	return nil
}

// lockFile is the on-disk JSON payload.
type lockFile struct {
	Service         string    `json:"service"`
	Name            string    `json:"name,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	OwnerPID        int       `json:"owner_pid"`
	OwnerStartTicks uint64    `json:"owner_start_ticks"`
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// ProcessProber answers liveness questions about a lock owner. It is an
// interface so tests can model dead PIDs and PID reuse without real processes.
type ProcessProber interface {
	// Alive reports whether a process with pid currently exists.
	Alive(pid int) bool
	// StartTicks returns the owner's start time (field 22 of /proc/<pid>/stat).
	// ok is false when it cannot be read.
	StartTicks(pid int) (ticks uint64, ok bool)
}

// toLock builds a Lock from an on-disk payload and computed state.
func toLock(lf lockFile, path string, state State, staleReason string) Lock {
	return Lock{
		Service:         lf.Service,
		Name:            lf.Name,
		Reason:          lf.Reason,
		OwnerPID:        lf.OwnerPID,
		OwnerStartTicks: lf.OwnerStartTicks,
		CreatedAt:       lf.CreatedAt,
		ExpiresAt:       lf.ExpiresAt,
		Path:            path,
		State:           state,
		StaleReason:     staleReason,
	}
}

// classify computes a lock's state from its payload, the current time and a
// process prober.
func classify(lf lockFile, now time.Time, proc ProcessProber) (State, string) {
	if !lf.ExpiresAt.IsZero() && !now.Before(lf.ExpiresAt) {
		return StateExpired, "expired"
	}
	if lf.OwnerPID > 0 {
		if !proc.Alive(lf.OwnerPID) {
			return StateStale, "dead owner"
		}
		if ticks, ok := proc.StartTicks(lf.OwnerPID); ok && ticks != lf.OwnerStartTicks {
			return StateStale, "pid reuse"
		}
	}
	return StateActive, ""
}
