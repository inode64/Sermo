package app

import "sermo/internal/rules"

// RemediationRegistry holds each service's latest remediation policy view for the
// web detail. Workers publish after every cycle; the web reads.
type RemediationRegistry registry[rules.RemediationReport]

// NewRemediationRegistry returns an empty registry.
func NewRemediationRegistry() *RemediationRegistry {
	return (*RemediationRegistry)(newRegistry[rules.RemediationReport]())
}

// Publish replaces the latest immutable snapshot for service.
func (r *RemediationRegistry) Publish(service string, value rules.RemediationReport) {
	(*registry[rules.RemediationReport])(r).Publish(service, value)
}

// Get returns the latest snapshot, or false when the service is unobserved.
func (r *RemediationRegistry) Get(service string) (rules.RemediationReport, bool) {
	var value rules.RemediationReport
	ok := (*registry[rules.RemediationReport])(r).get(service, &value)
	return value, ok
}
