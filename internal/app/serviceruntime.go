package app

import (
	"context"
	"sort"
	"sync"
	"time"

	"sermo/internal/metrics"
	"sermo/internal/process"
	"sermo/internal/web"
)

// ServiceMetricSampler stores per-service process-tree runtime samples for the
// web detail graphs. Workers record into it every observed cycle; the web
// backend reads the same in-memory history when a service detail is expanded.
type ServiceMetricSampler struct {
	mu      sync.Mutex
	prev    map[string]serviceMetricCounters
	samples map[string][]serviceMetricSample
}

type serviceMetricCounters struct {
	at      time.Time
	ioRead  int64
	ioWrite int64
	ok      bool
}

type serviceMetricSample struct {
	at      time.Time
	current web.ServiceRuntime
}

// NewServiceMetricSampler returns an empty service runtime metric history.
func NewServiceMetricSampler() *ServiceMetricSampler {
	return &ServiceMetricSampler{
		prev:    map[string]serviceMetricCounters{},
		samples: map[string][]serviceMetricSample{},
	}
}

// Record appends one process-tree runtime sample for a service and returns the
// same sample after rate fields (currently IO) have been computed.
func (s *ServiceMetricSampler) Record(name string, cur web.ServiceRuntime) web.ServiceRuntime {
	if s == nil {
		return cur
	}
	at, err := time.Parse(time.RFC3339, cur.At)
	if err != nil {
		at = time.Now()
		cur.At = at.UTC().Format(time.RFC3339)
	}

	s.mu.Lock()
	cur = s.recordLocked(name, cur, at)
	s.mu.Unlock()
	return cur
}

func (s *ServiceMetricSampler) recordLocked(name string, cur web.ServiceRuntime, at time.Time) web.ServiceRuntime {
	prev := s.prev[name]
	if prev.ok && cur.Count > 0 && cur.IORead >= prev.ioRead && cur.IOWrite >= prev.ioWrite {
		wall := at.Sub(prev.at).Seconds()
		if wall > 0 {
			cur.IOReadRate = float64(cur.IORead-prev.ioRead) / wall
			cur.IOWriteRate = float64(cur.IOWrite-prev.ioWrite) / wall
			cur.IORate = cur.IOReadRate + cur.IOWriteRate
			cur.IOReady = true
		}
	}
	s.prev[name] = serviceMetricCounters{at: at, ioRead: cur.IORead, ioWrite: cur.IOWrite, ok: cur.Count > 0}
	s.samples[name] = append(s.samples[name], serviceMetricSample{at: at, current: cur})
	s.trimLocked(name, at.Add(-daemonMetricRetention))
	return cur
}

// Series records the current runtime sample and returns the selected historical
// window as web metric series for CPU, memory and IO.
func (s *ServiceMetricSampler) Series(name string, cur web.ServiceRuntime, since time.Duration) web.ServiceRuntimeMetrics {
	if s == nil {
		return web.ServiceRuntimeMetrics{Since: since.String(), Current: cur}
	}
	at, err := time.Parse(time.RFC3339, cur.At)
	if err != nil {
		at = time.Now()
		cur.At = at.UTC().Format(time.RFC3339)
	}

	s.mu.Lock()
	cur = s.recordLocked(name, cur, at)
	samples := serviceSamplesSince(s.samples[name], at.Add(-since))
	s.mu.Unlock()
	return web.ServiceRuntimeMetrics{
		Since:   since.String(),
		Current: cur,
		CPU:     serviceMetricSeries("cpu", "%", since, samples, func(p serviceMetricSample) (float64, bool) { return p.current.CPU, p.current.HasCPU }),
		Memory:  serviceMetricSeries("memory", "bytes", since, samples, func(p serviceMetricSample) (float64, bool) { return float64(p.current.RSS), p.current.Count > 0 }),
		IO:      serviceMetricSeries("io", "B/s", since, samples, func(p serviceMetricSample) (float64, bool) { return p.current.IORate, p.current.IOReady }),
	}
}

func (s *ServiceMetricSampler) trimLocked(name string, cutoff time.Time) {
	samples := s.samples[name]
	i := 0
	for i < len(samples) && samples[i].at.Before(cutoff) {
		i++
	}
	if i > 0 {
		copy(samples, samples[i:])
		s.samples[name] = samples[:len(samples)-i]
	}
}

func serviceSamplesSince(samples []serviceMetricSample, cutoff time.Time) []serviceMetricSample {
	out := make([]serviceMetricSample, 0, len(samples))
	for _, sample := range samples {
		if !sample.at.Before(cutoff) {
			out = append(out, sample)
		}
	}
	return out
}

func serviceMetricSeries(metric, unit string, since time.Duration, samples []serviceMetricSample, value func(serviceMetricSample) (float64, bool)) web.MetricSeries {
	byMinute := map[time.Time]*daemonMetricAgg{}
	var summary daemonMetricAgg
	for _, sample := range samples {
		v, ok := value(sample)
		if !ok {
			continue
		}
		addDaemonMetric(&summary, v)
		minute := sample.at.UTC().Truncate(time.Minute)
		agg := byMinute[minute]
		if agg == nil {
			agg = &daemonMetricAgg{}
			byMinute[minute] = agg
		}
		addDaemonMetric(agg, v)
	}

	keys := make([]time.Time, 0, len(byMinute))
	for k := range byMinute {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Before(keys[j]) })

	points := make([]web.MetricPoint, 0, len(keys))
	for _, k := range keys {
		agg := byMinute[k]
		points = append(points, web.MetricPoint{
			Start: k.Format(time.RFC3339),
			N:     agg.n,
			Avg:   agg.sum / float64(agg.n),
			Min:   agg.min,
			Max:   agg.max,
		})
	}
	return web.MetricSeries{
		Check:   "runtime",
		Metric:  metric,
		Since:   since.String(),
		Unit:    unit,
		Summary: daemonMetricSummary(summary),
		Points:  points,
	}
}

// ServiceRuntime returns current and historical process-tree metrics for one service.
func (b *WebBackend) ServiceRuntime(_ context.Context, name string, since time.Duration) (web.ServiceRuntimeMetrics, bool) {
	e := b.entries[name]
	if e == nil {
		return web.ServiceRuntimeMetrics{}, false
	}
	cur := b.currentServiceRuntime(name, e)
	if b.serviceMetrics == nil {
		return web.ServiceRuntimeMetrics{Since: since.String(), Current: cur}, true
	}
	return b.serviceMetrics.Series(name, cur, since), true
}

func (b *WebBackend) decorateServiceRuntime(name string, e *webEntry, svc *web.Service) {
	if svc == nil || e == nil || e.disabled {
		return
	}
	cur := b.currentServiceRuntime(name, e)
	svc.StartedAt = cur.StartedAt
	svc.Uptime = cur.Uptime
	svc.UptimeSeconds = cur.UptimeSeconds
	svc.ProcessCount = cur.Count
	svc.RSS = cur.RSS
	svc.IORead = cur.IORead
	svc.IOWrite = cur.IOWrite
	svc.FDs = cur.FDs
	svc.Threads = cur.Threads
	svc.CPU = cur.CPU
	svc.CPUThread = cur.CPUThread
	svc.NumCPU = cur.NumCPU
	svc.CPUReady = cur.HasCPU
}

func (b *WebBackend) currentServiceRuntime(name string, e *webEntry) web.ServiceRuntime {
	now := time.Now()
	if b.now != nil {
		now = b.now()
	}
	cur := web.ServiceRuntime{At: now.UTC().Format(time.RFC3339)}
	if e == nil || e.disabled || e.noResidentProcess {
		return cur
	}
	procs, _ := e.discoverer.Discover(e.selectors)
	_, totals := aggregateProcesses(procs, b.runtimeMetricReader())
	if totals != nil {
		attachLiveTotals(totals, b.live, name)
		cur.ProcessTotals = *totals
	}
	if started, ok := oldestProcessStart(procs, b.runtimeMetricReader(), now); ok {
		cur.StartedAt = started.UTC().Format(time.RFC3339)
		secs := int64(now.Sub(started).Seconds())
		if secs < 0 {
			secs = 0
		}
		cur.UptimeSeconds = secs
		cur.Uptime = formatInterval(time.Duration(secs) * time.Second)
	}
	return cur
}

func (b *WebBackend) runtimeMetricReader() metrics.Reader {
	if b != nil && b.collector != nil && b.collector.Reader != nil {
		return b.collector.Reader
	}
	return metrics.OSReader{}
}

type processStartReader interface {
	ProcessStartTime(pid int) (time.Time, bool)
}

func oldestProcessStart(procs []process.Process, r metrics.Reader, now time.Time) (time.Time, bool) {
	sr, ok := r.(processStartReader)
	if !ok {
		return time.Time{}, false
	}
	var oldest time.Time
	for _, p := range procs {
		started, ok := sr.ProcessStartTime(p.PID)
		if !ok || started.After(now) {
			continue
		}
		if oldest.IsZero() || started.Before(oldest) {
			oldest = started
		}
	}
	if oldest.IsZero() {
		return time.Time{}, false
	}
	return oldest, true
}
