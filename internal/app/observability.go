package app

import "time"

// ObservabilityRegistry tracks services whose monitoring data has completed at
// least one normal observed cycle. It is process-local by design: persisted SLA,
// check and metric history remain in the state store, while readiness is about
// the current daemon generation having fresh indicators to show.
type ObservabilityRegistry registry[time.Time]

// NewObservabilityRegistry returns an empty service observability registry.
func NewObservabilityRegistry() *ObservabilityRegistry {
	return (*ObservabilityRegistry)(newRegistry[time.Time]())
}

// MarkReady records that service observability is ready at the given time.
func (r *ObservabilityRegistry) MarkReady(service string, at time.Time) {
	if r == nil || service == "" {
		return
	}
	(*registry[time.Time])(r).Publish(service, at)
}

// Clear removes service observability readiness.
func (r *ObservabilityRegistry) Clear(service string) {
	if r == nil || service == "" {
		return
	}
	(*registry[time.Time])(r).Clear(service)
}

// Ready reports when service observability became ready.
func (r *ObservabilityRegistry) Ready(service string) (time.Time, bool) {
	if r == nil || service == "" {
		return time.Time{}, false
	}
	var at time.Time
	ok := (*registry[time.Time])(r).get(service, &at)
	return at, ok
}
