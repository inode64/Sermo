package app

import (
	"context"
	"os"
	"sort"
	"sync"
	"time"

	"sermo/internal/metrics"
	"sermo/internal/state"
	"sermo/internal/web"
)

const (
	daemonMetricMaxInt64              = uint64(1<<63 - 1)
	daemonMetricRetention             = state.DefaultRetention1d
	metricSeriesBucket                = time.Minute
	defaultDaemonMetricSampleInterval = 30 * time.Second
)

// DaemonMetricSampler records sermod process metrics independently from web
// requests and serves their current and historical representation.
type DaemonMetricSampler struct {
	reader metrics.Reader
	store  DaemonMetricStore
	now    func() time.Time
	pid    int

	mu      sync.Mutex
	prev    daemonMetricCounters
	samples []daemonMetricSample
}

type daemonMetricCounters struct {
	at       time.Time
	cpuTicks uint64
	ioRead   uint64
	ioWrite  uint64
	cpuOK    bool
	ioOK     bool
}

type daemonMetricSample struct {
	at            time.Time
	pid           int
	rss           uint64
	rssOK         bool
	memoryPercent float64
	memoryPctOK   bool
	cpu           float64
	cpuReady      bool
	ioRead        float64
	ioWrite       float64
	io            float64
	ioReady       bool
	fds           uint64
	fdsOK         bool
	threads       uint64
	threadsOK     bool
	numCPU        int
}

type daemonMetricAgg struct {
	n   int64
	sum float64
	min float64
	max float64
}

type persistentMetricValue struct {
	name  string
	value float64
	ready bool
}

type persistentMetricReader struct {
	summary func(metric string, span time.Duration, now time.Time) (state.MeasurementStat, error)
	series  func(metric string, from, to time.Time) ([]state.MeasurementPoint, error)
}

// persistentMetricTriplet is the CPU/memory/IO series set every metric surface
// reports, however it was sourced: loadPersistentMetricTriplet reads it back
// from the store, sampledMetricTriplet aggregates it from in-memory samples.
type persistentMetricTriplet struct {
	cpu    web.MetricSeries
	memory web.MetricSeries
	io     web.MetricSeries
}

// sampledMetricTriplet aggregates the in-memory samples into the same triplet
// loadPersistentMetricTriplet returns, for the path taken before a store is
// configured. Together with that function it is the only place pairing each
// metric with its unit, so daemon and service runtime series cannot drift.
func sampledMetricTriplet[T any](check string, since time.Duration, samples []T,
	at func(T) time.Time, cpu, memory, io func(T) (float64, bool),
) persistentMetricTriplet {
	return persistentMetricTriplet{
		cpu:    metricSeries(check, metrics.MetricCPU, metrics.MetricUnitPercent, since, samples, at, cpu),
		memory: metricSeries(check, metrics.MetricMemory, metrics.MetricUnitBytes, since, samples, at, memory),
		io:     metricSeries(check, metrics.MetricIO, metrics.MetricUnitBytesPerSecond, since, samples, at, io),
	}
}

// NewDaemonMetricSampler builds the process metric sampler shared by sermod and
// the web backend.
func NewDaemonMetricSampler(collector *metrics.Collector, now func() time.Time, store DaemonMetricStore) *DaemonMetricSampler {
	reader := metrics.Reader(metrics.OSReader{})
	if collector != nil && collector.Reader != nil {
		reader = collector.Reader
	}
	if now == nil {
		now = time.Now
	}
	return &DaemonMetricSampler{reader: reader, store: store, now: now, pid: os.Getpid()}
}

// Run samples immediately and then at interval until ctx is cancelled.
func (s *DaemonMetricSampler) Run(ctx context.Context, interval time.Duration) {
	if s == nil {
		return
	}
	if interval <= 0 {
		interval = defaultDaemonMetricSampleInterval
	}
	s.sampleWithContext(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sampleWithContext(ctx)
		}
	}
}

func (s *DaemonMetricSampler) sample() {
	s.sampleWithContext(context.Background())
}

func (s *DaemonMetricSampler) sampleWithContext(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	sample := s.sampleLocked()
	s.samples = append(s.samples, sample)
	s.trimLocked(sample.at.Add(-daemonMetricRetention))
	s.mu.Unlock()
	s.recordPersistent(ctx, sample)
}

// Series returns current and historical daemon metrics without taking or
// recording a new sample.
func (s *DaemonMetricSampler) Series(since time.Duration) web.DaemonMetrics {
	if s == nil {
		return web.DaemonMetrics{Since: since.String()}
	}
	s.mu.Lock()
	if len(s.samples) == 0 {
		s.mu.Unlock()
		return web.DaemonMetrics{Since: since.String()}
	}
	sample := s.samples[len(s.samples)-1]
	samples := samplesSince(s.samples, sample.at.Add(-since))
	s.mu.Unlock()

	if s.store != nil {
		if out, ok := s.persistentSeries(sample, since); ok {
			return out
		}
	}
	triplet := sampledMetricTriplet(daemonMetricCheck, since, samples,
		func(p daemonMetricSample) time.Time { return p.at },
		func(p daemonMetricSample) (float64, bool) { return p.cpu, p.cpuReady },
		func(p daemonMetricSample) (float64, bool) { return float64(p.rss), p.rssOK },
		func(p daemonMetricSample) (float64, bool) { return p.io, p.ioReady },
	)
	return web.DaemonMetrics{
		Since:   since.String(),
		Current: daemonRuntime(sample),
		CPU:     triplet.cpu,
		Memory:  triplet.memory,
		IO:      triplet.io,
	}
}

func (s *DaemonMetricSampler) sampleLocked() daemonMetricSample {
	at := s.now()
	pid := s.pid
	cur := daemonMetricSample{at: at, pid: pid, numCPU: s.reader.NumCPU()}
	if rss, ok := s.reader.ProcessRSS(pid); ok {
		cur.rss = rss
		cur.rssOK = true
		if total, _, ok := s.reader.TotalMemory(); ok && total > 0 {
			cur.memoryPercent = float64(rss) / float64(total) * metrics.PercentScale
			cur.memoryPctOK = true
		}
	}
	fds, fdsOK := s.reader.ProcessFDs(pid)
	cur.fds, cur.fdsOK = fds, fdsOK
	threads, threadsOK := s.reader.ProcessThreads(pid)
	cur.threads, cur.threadsOK = threads, threadsOK

	counters := daemonMetricCounters{at: at}
	if ticks, ok := s.reader.ProcessCPU(pid); ok {
		counters.cpuTicks = ticks
		counters.cpuOK = true
		if s.prev.cpuOK && ticks >= s.prev.cpuTicks {
			wall := at.Sub(s.prev.at).Seconds()
			hz := s.reader.ClockTicks()
			ncpu := cur.numCPU
			if wall > 0 && hz > 0 && ncpu > 0 {
				cur.cpu = float64(ticks-s.prev.cpuTicks) / hz / wall / float64(ncpu) * metrics.PercentScale
				cur.cpuReady = true
			}
		}
	}
	if rd, wr, ok := s.reader.ProcessIO(pid); ok {
		counters.ioRead = rd
		counters.ioWrite = wr
		counters.ioOK = true
		if s.prev.ioOK && rd >= s.prev.ioRead && wr >= s.prev.ioWrite {
			wall := at.Sub(s.prev.at).Seconds()
			if wall > 0 {
				cur.ioRead = float64(rd-s.prev.ioRead) / wall
				cur.ioWrite = float64(wr-s.prev.ioWrite) / wall
				cur.io = cur.ioRead + cur.ioWrite
				cur.ioReady = true
			}
		}
	}
	s.prev = counters
	return cur
}

func (s *DaemonMetricSampler) recordPersistent(ctx context.Context, sample daemonMetricSample) {
	if s.store == nil {
		return
	}
	values := [3]persistentMetricValue{
		{name: metrics.MetricMemory, value: float64(sample.rss), ready: sample.rssOK},
		{name: metrics.MetricCPU, value: sample.cpu, ready: sample.cpuReady},
		{name: metrics.MetricIO, value: sample.io, ready: sample.ioReady},
	}
	if !hasReadyPersistentMetric(values) {
		return
	}
	if recordPersistentMetricsWithBatch(ctx, s.store, func(records state.Batch) persistentMetricRecorder {
		return records.RecordDaemonMetric
	}, sample.at, values) {
		return
	}
	_ = recordPersistentMetrics(s.store.RecordDaemonMetric, sample.at, values)
}

func (s *DaemonMetricSampler) persistentSeries(sample daemonMetricSample, since time.Duration) (web.DaemonMetrics, bool) {
	if s.store == nil {
		return web.DaemonMetrics{}, false
	}
	triplet, ok := loadPersistentMetricTriplet(daemonMetricCheck, sample.at, since, persistentMetricReader{
		summary: s.store.DaemonMetricSummary,
		series:  s.store.DaemonMetricSeries,
	})
	if !ok {
		return web.DaemonMetrics{}, false
	}
	return web.DaemonMetrics{
		Since:   since.String(),
		Current: daemonRuntime(sample),
		CPU:     triplet.cpu,
		Memory:  triplet.memory,
		IO:      triplet.io,
	}, true
}

type persistentMetricRecorder func(string, float64, time.Time) error

func hasReadyPersistentMetric(values [3]persistentMetricValue) bool {
	for _, value := range values {
		if value.ready {
			return true
		}
	}
	return false
}

// recordPersistentMetrics records every ready value and returns the first
// error. Direct callers intentionally ignore it to retain their best-effort
// behavior; batch callers use it to roll back the complete metric sample.
func recordPersistentMetrics(record persistentMetricRecorder, at time.Time, values [3]persistentMetricValue) error {
	var firstErr error
	for _, value := range values {
		if value.ready {
			if err := record(value.name, value.value, at); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// recordPersistentMetricsWithBatch records one daemon or service metric sample
// atomically when the store supports batches. It reports whether it used that
// path; callers retain the direct best-effort fallback for test stores.
func recordPersistentMetricsWithBatch(ctx context.Context, store any, record func(state.Batch) persistentMetricRecorder, at time.Time, values [3]persistentMetricValue) bool {
	batch, ok := store.(stateBatchStore)
	if !ok {
		return false
	}
	_ = batch.WithBatch(ctx, func(records state.Batch) error {
		return recordPersistentMetrics(record(records), at, values)
	})
	return true
}

func loadPersistentMetricTriplet(check string, at time.Time, since time.Duration, reader persistentMetricReader) (persistentMetricTriplet, bool) {
	now := at.Add(metricSeriesBucket)
	load := func(metric, unit string) (web.MetricSeries, bool) {
		stat, err := reader.summary(metric, since+metricSeriesBucket, now)
		if err != nil {
			return web.MetricSeries{}, false
		}
		points, err := reader.series(metric, at.Add(-since), now)
		if err != nil {
			return web.MetricSeries{}, false
		}
		return web.MetricSeries{
			Check:   check,
			Metric:  metric,
			Since:   since.String(),
			Unit:    unit,
			Summary: metricSummary(stat),
			Points:  measurementPoints(points),
		}, true
	}
	cpu, ok := load(metrics.MetricCPU, metrics.MetricUnitPercent)
	if !ok {
		return persistentMetricTriplet{}, false
	}
	memory, ok := load(metrics.MetricMemory, metrics.MetricUnitBytes)
	if !ok {
		return persistentMetricTriplet{}, false
	}
	io, ok := load(metrics.MetricIO, metrics.MetricUnitBytesPerSecond)
	if !ok {
		return persistentMetricTriplet{}, false
	}
	return persistentMetricTriplet{cpu: cpu, memory: memory, io: io}, true
}

func (s *DaemonMetricSampler) trimLocked(cutoff time.Time) {
	s.samples = trimBefore(s.samples, cutoff, func(sample daemonMetricSample) time.Time { return sample.at })
}

func samplesSince(samples []daemonMetricSample, cutoff time.Time) []daemonMetricSample {
	return filterSince(samples, cutoff, func(s daemonMetricSample) time.Time { return s.at })
}

func daemonRuntime(sample daemonMetricSample) web.DaemonRuntime {
	out := web.DaemonRuntime{
		At:       sample.at.UTC().Format(time.RFC3339),
		PID:      sample.pid,
		CPU:      sample.cpu,
		CPUReady: sample.cpuReady,
		IORead:   sample.ioRead,
		IOWrite:  sample.ioWrite,
		IO:       sample.io,
		IOReady:  sample.ioReady,
		NumCPU:   sample.numCPU,
	}
	if sample.rssOK {
		out.RSS = uintToInt64(sample.rss)
	}
	if sample.memoryPctOK {
		out.MemoryPercent = sample.memoryPercent
	}
	if sample.fdsOK {
		out.FDs = uintToInt64(sample.fds)
	}
	if sample.threadsOK {
		out.Threads = uintToInt64(sample.threads)
	}
	return out
}

func metricSeries[T any](check, metric, unit string, since time.Duration, samples []T, at func(T) time.Time, value func(T) (float64, bool)) web.MetricSeries {
	// Aggregates are stored by value: the zero value is a valid empty bucket, so
	// no bucket can be nil and absent minutes need no allocation.
	byMinute := map[time.Time]daemonMetricAgg{}
	var summary daemonMetricAgg
	for _, sample := range samples {
		v, ok := value(sample)
		if !ok {
			continue
		}
		summary = addDaemonMetric(summary, v)
		minute := at(sample).UTC().Truncate(metricSeriesBucket)
		byMinute[minute] = addDaemonMetric(byMinute[minute], v)
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
		Check:   check,
		Metric:  metric,
		Since:   since.String(),
		Unit:    unit,
		Summary: daemonMetricSummary(summary),
		Points:  points,
	}
}

// addDaemonMetric folds value into agg and returns the updated aggregate. It
// takes and returns the struct by value so buckets are never pointers: the zero
// aggregate is a valid empty bucket, which keeps every caller nil-free.
func addDaemonMetric(agg daemonMetricAgg, value float64) daemonMetricAgg {
	if agg.n == 0 {
		agg.min, agg.max = value, value
	} else {
		if value < agg.min {
			agg.min = value
		}
		if value > agg.max {
			agg.max = value
		}
	}
	agg.n++
	agg.sum += value
	return agg
}

func daemonMetricSummary(agg daemonMetricAgg) web.MetricSummary {
	if agg.n == 0 {
		return web.MetricSummary{}
	}
	return web.MetricSummary{
		Count: agg.n,
		Avg:   agg.sum / float64(agg.n),
		Min:   agg.min,
		Max:   agg.max,
	}
}

func metricSummary(stat state.MeasurementStat) web.MetricSummary {
	return web.MetricSummary{Count: stat.Count, Avg: stat.Avg, Min: stat.Min, Max: stat.Max}
}

func uintToInt64(v uint64) int64 {
	if v > daemonMetricMaxInt64 {
		return int64(daemonMetricMaxInt64)
	}
	return int64(v)
}
