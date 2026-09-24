package metrics

import "sync"

// processMetricReader contains the readings shared by service checks and live
// views. Optional swap and limit capabilities remain on the original Reader.
type processMetricReader interface {
	ProcessCPU(pid int) (uint64, bool)
	ProcessRSS(pid int) (uint64, bool)
	ProcessIO(pid int) (uint64, uint64, bool)
	ProcessFDs(pid int) (uint64, bool)
	ProcessThreads(pid int) (uint64, bool)
}

// ProcessObservation lazily retains raw process readings for one monitoring
// cycle. Failed reads are retained too; a new observation retries them. Rate
// baselines belong to the collectors and are never shared through this cache.
type ProcessObservation struct {
	reader Reader
	pids   []int
	mu     sync.Mutex
	values map[processMetricKey]processReading
}

type processMetricKey struct {
	pid    int
	metric string
}

type processReading struct {
	value, write uint64
	ticks        map[int]uint64
	ok           bool
}

// NewProcessObservation starts an empty observation over the discovered PID set.
// It performs no reads until a consumer requests them.
func NewProcessObservation(reader Reader, pids []int) *ProcessObservation {
	return &ProcessObservation{reader: reader, pids: pids, values: map[processMetricKey]processReading{}}
}

func (o *ProcessObservation) read(pid int, metric string) processReading {
	o.mu.Lock()
	defer o.mu.Unlock()
	key := processMetricKey{pid: pid, metric: metric}
	if value, ok := o.values[key]; ok {
		return value
	}
	var value processReading
	switch metric {
	case MetricCPU:
		value.value, value.ok = o.reader.ProcessCPU(pid)
	case MetricMemory:
		value.value, value.ok = o.reader.ProcessRSS(pid)
	case MetricFds:
		value.value, value.ok = o.reader.ProcessFDs(pid)
	case MetricThreads:
		value.value, value.ok = o.reader.ProcessThreads(pid)
	case MetricIO:
		value.value, value.write, value.ok = o.reader.ProcessIO(pid)
	case MetricCPUThread:
		if reader, ok := o.reader.(threadCPUReader); ok {
			value.ticks, value.ok = reader.ProcessThreadCPU(pid)
		}
	}
	o.values[key] = value
	return value
}

// ProcessCPU returns the cycle's cached CPU reading, including availability.
func (o *ProcessObservation) ProcessCPU(pid int) (uint64, bool) {
	r := o.read(pid, MetricCPU)
	return r.value, r.ok
}

// ProcessRSS returns the cycle's cached memory reading, including availability.
func (o *ProcessObservation) ProcessRSS(pid int) (uint64, bool) {
	r := o.read(pid, MetricMemory)
	return r.value, r.ok
}

// ProcessFDs returns the cycle's cached descriptor count, including availability.
func (o *ProcessObservation) ProcessFDs(pid int) (uint64, bool) {
	r := o.read(pid, MetricFds)
	return r.value, r.ok
}

// ProcessThreads returns the cycle's cached thread count, including availability.
func (o *ProcessObservation) ProcessThreads(pid int) (uint64, bool) {
	r := o.read(pid, MetricThreads)
	return r.value, r.ok
}

// ProcessIO returns the cycle's cumulative IO counters.
func (o *ProcessObservation) ProcessIO(pid int) (uint64, uint64, bool) {
	r := o.read(pid, MetricIO)
	return r.value, r.write, r.ok
}

// ProcessThreadCPU reads threads only when a collector's sampling floor asks
// for them, and shares that reading with other collectors in the same cycle.
func (o *ProcessObservation) ProcessThreadCPU(pid int) (map[int]uint64, bool) {
	r := o.read(pid, MetricCPUThread)
	return r.ticks, r.ok
}
