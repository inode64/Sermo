package app

import (
	"sync"
	"time"

	"sermo/internal/rules"
)

// RemediationRegistry holds each service's latest remediation policy view for the
// web detail. Workers publish after every cycle; the web reads.
type RemediationRegistry struct {
	mu        sync.RWMutex
	byService map[string]rules.RemediationReport
}

// NewRemediationRegistry returns an empty registry.
func NewRemediationRegistry() *RemediationRegistry {
	return &RemediationRegistry{byService: map[string]rules.RemediationReport{}}
}

// Publish snapshots policy gating for service.
func (r *RemediationRegistry) Publish(service string, policy rules.Policy, state *rules.RemediationState, now time.Time) {
	if r == nil {
		return
	}
	snap := policy.Report(state, now)
	r.mu.Lock()
	r.byService[service] = snap
	r.mu.Unlock()
}

// Get returns the last published report for service, or false when none yet.
func (r *RemediationRegistry) Get(service string) (rules.RemediationReport, bool) {
	if r == nil {
		return rules.RemediationReport{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	rep, ok := r.byService[service]
	return rep, ok
}
