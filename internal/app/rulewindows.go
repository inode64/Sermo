package app

import (
	"sync"

	"sermo/internal/rules"
)

// RuleWindowRegistry holds each service's latest rule window snapshot for the
// web detail. Workers publish after every observed cycle; the web reads.
type RuleWindowRegistry struct {
	mu        sync.RWMutex
	byService map[string][]rules.RuleWindowReport
}

// NewRuleWindowRegistry returns an empty registry.
func NewRuleWindowRegistry() *RuleWindowRegistry {
	return &RuleWindowRegistry{byService: map[string][]rules.RuleWindowReport{}}
}

// Publish snapshots rule window progress for service.
func (r *RuleWindowRegistry) Publish(service string, reports []rules.RuleWindowReport) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.byService[service] = reports
	r.mu.Unlock()
}

// Get returns the last published reports for service, or false when none yet.
func (r *RuleWindowRegistry) Get(service string) ([]rules.RuleWindowReport, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	rep, ok := r.byService[service]
	return rep, ok
}