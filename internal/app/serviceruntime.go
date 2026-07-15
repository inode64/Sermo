package app

import (
	"context"
	"strings"
	"sync"
	"time"

	"sermo/internal/metrics"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
	"sermo/internal/web"
)

// minRuntimePublishMaxAge is the minimum age after which a worker-published
// runtime sample is treated as stale for the service list (fallback to probe).
const (
	minRuntimePublishMaxAge    = 30 * time.Second
	runtimePublishMaxAgeCycles = 2
)

// runtimePublishMaxAge returns how long the web list reuses a worker-published
// runtime sample before probing again. It tracks twice the service cycle interval.
func runtimePublishMaxAge(interval time.Duration) time.Duration {
	if interval <= 0 {
		interval = minRuntimePublishMaxAge
	}
	age := interval * runtimePublishMaxAgeCycles
	if age < minRuntimePublishMaxAge {
		return minRuntimePublishMaxAge
	}
	return age
}

// ServiceMetricSampler stores per-service process-tree runtime samples for the
// web detail graphs. Workers record into it every observed cycle; when a store
// is wired, the history survives daemon restarts.
type ServiceMetricSampler struct {
	mu      sync.Mutex
	store   ServiceMetricStore
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

// NewServiceMetricSampler returns an empty service runtime metric history. The
// optional store persists CPU, memory and IO buckets across daemon restarts.
func NewServiceMetricSampler(stores ...ServiceMetricStore) *ServiceMetricSampler {
	var store ServiceMetricStore
	if len(stores) > 0 {
		store = stores[0]
	}
	return &ServiceMetricSampler{
		store:   store,
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
	s.recordPersistent(name, cur, at)
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

// Latest returns the most recent worker-published runtime sample.
func (s *ServiceMetricSampler) Latest(name string) (web.ServiceRuntime, bool) {
	cur, _, ok := s.LatestWithAt(name)
	return cur, ok
}

// LatestWithAt returns the latest worker-published sample and its observation time.
func (s *ServiceMetricSampler) LatestWithAt(name string) (web.ServiceRuntime, time.Time, bool) {
	if s == nil {
		return web.ServiceRuntime{}, time.Time{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	samples := s.samples[name]
	if len(samples) == 0 {
		return web.ServiceRuntime{}, time.Time{}, false
	}
	last := samples[len(samples)-1]
	return last.current, last.at, true
}

// Series returns the selected historical window for CPU, memory and IO without
// recording cur. Worker cycles own history sampling; dashboard reads must not
// change sample counts or weight averages by the number of connected clients.
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
	samples := serviceSamplesSince(s.samples[name], at.Add(-since))
	s.mu.Unlock()
	if out, ok := s.persistentSeries(name, cur, at, since); ok {
		return out
	}
	return web.ServiceRuntimeMetrics{
		Since:   since.String(),
		Current: cur,
		CPU: metricSeries(
			runtimeMetricCheck, metrics.MetricCPU, metrics.MetricUnitPercent, since, samples,
			func(p serviceMetricSample) time.Time { return p.at },
			func(p serviceMetricSample) (float64, bool) { return p.current.CPU, p.current.HasCPU },
		),
		Memory: metricSeries(
			runtimeMetricCheck, metrics.MetricMemory, metrics.MetricUnitBytes, since, samples,
			func(p serviceMetricSample) time.Time { return p.at },
			func(p serviceMetricSample) (float64, bool) { return float64(p.current.RSS), p.current.Count > 0 },
		),
		IO: metricSeries(
			runtimeMetricCheck, metrics.MetricIO, metrics.MetricUnitBytesPerSecond, since, samples,
			func(p serviceMetricSample) time.Time { return p.at },
			func(p serviceMetricSample) (float64, bool) { return p.current.IORate, p.current.IOReady },
		),
	}
}

func (s *ServiceMetricSampler) recordPersistent(name string, cur web.ServiceRuntime, at time.Time) {
	if s == nil || s.store == nil {
		return
	}
	if cur.HasCPU {
		_ = s.store.RecordServiceMetric(name, metrics.MetricCPU, cur.CPU, at)
	}
	if cur.Count > 0 {
		_ = s.store.RecordServiceMetric(name, metrics.MetricMemory, float64(cur.RSS), at)
	}
	if cur.IOReady {
		_ = s.store.RecordServiceMetric(name, metrics.MetricIO, cur.IORate, at)
	}
}

func (s *ServiceMetricSampler) persistentSeries(name string, cur web.ServiceRuntime, at time.Time, since time.Duration) (web.ServiceRuntimeMetrics, bool) {
	if s == nil || s.store == nil {
		return web.ServiceRuntimeMetrics{}, false
	}
	now := at.Add(metricSeriesBucket)
	series := func(metric, unit string) (web.MetricSeries, bool) {
		stat, err := s.store.ServiceMetricSummary(name, metric, since+metricSeriesBucket, now)
		if err != nil {
			return web.MetricSeries{}, false
		}
		points, err := s.store.ServiceMetricSeries(name, metric, at.Add(-since), now)
		if err != nil {
			return web.MetricSeries{}, false
		}
		return web.MetricSeries{
			Check:   runtimeMetricCheck,
			Metric:  metric,
			Since:   since.String(),
			Unit:    unit,
			Summary: metricSummary(stat),
			Points:  measurementPoints(points),
		}, true
	}
	cpu, ok := series(metrics.MetricCPU, metrics.MetricUnitPercent)
	if !ok {
		return web.ServiceRuntimeMetrics{}, false
	}
	memory, ok := series(metrics.MetricMemory, metrics.MetricUnitBytes)
	if !ok {
		return web.ServiceRuntimeMetrics{}, false
	}
	io, ok := series(metrics.MetricIO, metrics.MetricUnitBytesPerSecond)
	if !ok {
		return web.ServiceRuntimeMetrics{}, false
	}
	return web.ServiceRuntimeMetrics{
		Since:   since.String(),
		Current: cur,
		CPU:     cpu,
		Memory:  memory,
		IO:      io,
	}, true
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
	for i := range samples {
		if !samples[i].at.Before(cutoff) {
			out = append(out, samples[i])
		}
	}
	return out
}

// ServiceRuntime returns current and historical process-tree metrics for one service.
func (b *WebBackend) ServiceRuntime(_ context.Context, name string, since time.Duration) (web.ServiceRuntimeMetrics, bool) {
	e := b.entries[name]
	if e == nil || e.noResidentProcess {
		return web.ServiceRuntimeMetrics{}, false
	}
	cur := b.probeServiceRuntime(name, e)
	if b.serviceMetrics == nil {
		return web.ServiceRuntimeMetrics{Since: since.String(), Current: cur}, true
	}
	return b.serviceMetrics.Series(name, cur, since), true
}

func (b *WebBackend) decorateServiceRuntime(name string, e *webEntry, svc *web.Service) {
	if svc == nil || e == nil || e.disabled || e.noResidentProcess || !serviceRuntimeVisible(svc.Status) {
		return
	}
	applyServiceRuntimeFields(svc, b.listServiceRuntime(name, e))
}

func serviceRuntimeVisible(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case string(servicemgr.StatusActive), string(servicemgr.StatusPaused):
		return true
	default:
		return false
	}
}

func applyServiceRuntimeFields(svc *web.Service, cur web.ServiceRuntime) {
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

// listServiceRuntime returns runtime fields for the service list. It reuses the
// worker-published sample when fresh; otherwise it probes the process tree.
func (b *WebBackend) listServiceRuntime(name string, e *webEntry) web.ServiceRuntime {
	if cur, ok := b.publishedServiceRuntime(name, e); ok {
		return cur
	}
	return b.probeServiceRuntime(name, e)
}

func (b *WebBackend) publishedServiceRuntime(name string, e *webEntry) (web.ServiceRuntime, bool) {
	if e == nil || e.disabled || e.noResidentProcess {
		return web.ServiceRuntime{}, false
	}
	now := b.webNow()
	maxAge := runtimePublishMaxAge(e.interval)
	if b.serviceMetrics == nil {
		return web.ServiceRuntime{}, false
	}
	cur, at, ok := b.serviceMetrics.LatestWithAt(name)
	if !ok || now.Sub(at) > maxAge {
		return web.ServiceRuntime{}, false
	}
	attachLiveTotals(&cur.ProcessTotals, b.live, name)
	return cur, true
}

func (b *WebBackend) webNow() time.Time {
	if b != nil && b.now != nil {
		return b.now()
	}
	return time.Now()
}

func (b *WebBackend) probeServiceRuntime(name string, e *webEntry) web.ServiceRuntime {
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
		cur.StartedAt, cur.Uptime, cur.UptimeSeconds = serviceRuntimeUptime(started, now)
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

func oldestPIDStart(pids []int, r metrics.Reader, now time.Time) (time.Time, bool) {
	if len(pids) == 0 {
		return time.Time{}, false
	}
	procs := make([]process.Process, len(pids))
	for i, pid := range pids {
		procs[i] = process.Process{PID: pid}
	}
	return oldestProcessStart(procs, r, now)
}

func serviceRuntimeUptime(started, now time.Time) (startedAt, uptime string, uptimeSeconds int64) {
	if started.IsZero() || started.After(now) {
		return "", "", 0
	}
	secs := max(int64(now.Sub(started).Seconds()), 0)
	return started.UTC().Format(time.RFC3339), formatInterval(time.Duration(secs) * time.Second), secs
}

func oldestProcessStart(procs []process.Process, r metrics.Reader, now time.Time) (time.Time, bool) {
	sr, ok := r.(processStartReader)
	if !ok {
		return time.Time{}, false
	}
	var oldest time.Time
	for i := range procs {
		started, ok := sr.ProcessStartTime(procs[i].PID)
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
