package app

// ServiceLive is a service's most recent live CPU readings, published per cycle
// by its worker and read by the web detail view. CPU is the whole-machine rate
// (% of all cores); CPUThread is the busiest single thread against one core
// (100% = one saturated core); PerProcCPU maps each PID to its single-core rate.
// CPUReady is false until two samples exist (the first cycle has no delta to
// rate against); it gates both aggregate CPU readings from the same sample.
//
// PerProcMaxCore is CPUThread per process — the most one core gave that process —
// and PerProcMaxCoreExact says which of those figures were measured from the
// process's threads rather than bounded by its own rate, so the table can mark an
// estimate as one.
type ServiceLive struct {
	CPU                 float64
	CPUReady            bool
	CPUThread           float64
	NumCPU              int
	PerProcCPU          map[int]float64
	PerProcMaxCore      map[int]float64
	PerProcMaxCoreExact map[int]bool
}

// LiveMetrics holds each service's latest live CPU sample so the web UI can show
// per-process and aggregate CPU without re-sampling /proc (which would corrupt
// the engine's rate deltas). Workers publish after every cycle; the web reads.
// Safe for concurrent use, mirroring Snapshots.
type LiveMetrics registry[ServiceLive]

// NewLiveMetrics returns an empty registry.
func NewLiveMetrics() *LiveMetrics {
	return (*LiveMetrics)(newRegistry[ServiceLive]())
}

// Publish replaces the latest immutable snapshot for service.
func (r *LiveMetrics) Publish(service string, value ServiceLive) {
	(*registry[ServiceLive])(r).Publish(service, value)
}

// Get returns the latest snapshot, or false when the service is unobserved.
func (r *LiveMetrics) Get(service string) (ServiceLive, bool) {
	var value ServiceLive
	ok := (*registry[ServiceLive])(r).get(service, &value)
	return value, ok
}
