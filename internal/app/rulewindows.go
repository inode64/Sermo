package app

import "sermo/internal/rules"

// RuleWindowRegistry holds each service's latest rule window snapshot for the
// web detail. Workers publish after every observed cycle; the web reads.
type RuleWindowRegistry registry[[]rules.RuleWindowReport]

// NewRuleWindowRegistry returns an empty registry.
func NewRuleWindowRegistry() *RuleWindowRegistry {
	return (*RuleWindowRegistry)(newRegistry[[]rules.RuleWindowReport]())
}

// Publish replaces the latest immutable snapshot for service.
func (r *RuleWindowRegistry) Publish(service string, value []rules.RuleWindowReport) {
	(*registry[[]rules.RuleWindowReport])(r).Publish(service, value)
}

// Get returns the latest snapshot, or false when the service is unobserved.
func (r *RuleWindowRegistry) Get(service string) ([]rules.RuleWindowReport, bool) {
	var value []rules.RuleWindowReport
	ok := (*registry[[]rules.RuleWindowReport])(r).get(service, &value)
	return value, ok
}
