package app

import (
	"os"
	"sort"
	"sync"
	"time"

	"sermo/internal/metrics"
	"sermo/internal/state"
	"sermo/internal/web"
)

const daemonMetricRetention = 366 * 24 * time.Hour

type daemonMetricSampler struct {
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

func newDaemonMetricSampler(collector *metrics.Collector, now func() time.Time, store DaemonMetricStore) *daemonMetricSampler {
	reader := metrics.Reader(metrics.OSReader{})
	if collector != nil && collector.Reader != nil {
		reader = collector.Reader
	}
	if now == nil {
		now = time.Now
	}
	return &daemonMetricSampler{reader: reader, store: store, now: now, pid: os.Getpid()}
}

func (s *daemonMetricSampler) Series(since time.Duration) web.DaemonMetrics {
	if s == nil {
		return web.DaemonMetrics{Since: since.String()}
	}
	s.mu.Lock()
	sample := s.sampleLocked()
	s.samples = append(s.samples, sample)
	s.trimLocked(sample.at.Add(-daemonMetricRetention))
	samples := samplesSince(s.samples, sample.at.Add(-since))
	s.mu.Unlock()

	if s.store != nil {
		s.recordPersistent(sample)
		if out, ok := s.persistentSeries(sample, since); ok {
			return out
		}
	}
	return web.DaemonMetrics{
		Since:   since.String(),
		Current: daemonRuntime(sample),
		CPU:     daemonMetricSeries("cpu", "%", since, samples, func(p daemonMetricSample) (float64, bool) { return p.cpu, p.cpuReady }),
		Memory:  daemonMetricSeries("memory", "bytes", since, samples, func(p daemonMetricSample) (float64, bool) { return float64(p.rss), p.rssOK }),
		IO:      daemonMetricSeries("io", "B/s", since, samples, func(p daemonMetricSample) (float64, bool) { return p.io, p.ioReady }),
	}
}

func (s *daemonMetricSampler) sampleLocked() daemonMetricSample {
	at := s.now()
	pid := s.pid
	cur := daemonMetricSample{at: at, pid: pid, numCPU: s.reader.NumCPU()}
	if rss, ok := s.reader.ProcessRSS(pid); ok {
		cur.rss = rss
		cur.rssOK = true
		if total, _, ok := s.reader.TotalMemory(); ok && total > 0 {
			cur.memoryPercent = float64(rss) / float64(total) * 100
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
				cur.cpu = float64(ticks-s.prev.cpuTicks) / hz / wall / float64(ncpu) * 100
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

func (s *daemonMetricSampler) recordPersistent(sample daemonMetricSample) {
	if s.store == nil {
		return
	}
	if sample.rssOK {
		_ = s.store.RecordDaemonMetric("memory", float64(sample.rss), sample.at)
	}
	if sample.cpuReady {
		_ = s.store.RecordDaemonMetric("cpu", sample.cpu, sample.at)
	}
	if sample.ioReady {
		_ = s.store.RecordDaemonMetric("io", sample.io, sample.at)
	}
}

func (s *daemonMetricSampler) persistentSeries(sample daemonMetricSample, since time.Duration) (web.DaemonMetrics, bool) {
	if s.store == nil {
		return web.DaemonMetrics{}, false
	}
	now := sample.at.Add(time.Minute)
	series := func(metric, unit string) (web.MetricSeries, bool) {
		stat, err := s.store.DaemonMetricSummary(metric, since+time.Minute, now)
		if err != nil {
			return web.MetricSeries{}, false
		}
		points, err := s.store.DaemonMetricSeries(metric, sample.at.Add(-since), now)
		if err != nil {
			return web.MetricSeries{}, false
		}
		return web.MetricSeries{
			Check:   "sermod",
			Metric:  metric,
			Since:   since.String(),
			Unit:    unit,
			Summary: metricSummary(stat),
			Points:  measurementPoints(points),
		}, true
	}
	cpu, ok := series("cpu", "%")
	if !ok {
		return web.DaemonMetrics{}, false
	}
	memory, ok := series("memory", "bytes")
	if !ok {
		return web.DaemonMetrics{}, false
	}
	io, ok := series("io", "B/s")
	if !ok {
		return web.DaemonMetrics{}, false
	}
	return web.DaemonMetrics{
		Since:   since.String(),
		Current: daemonRuntime(sample),
		CPU:     cpu,
		Memory:  memory,
		IO:      io,
	}, true
}

func (s *daemonMetricSampler) trimLocked(cutoff time.Time) {
	i := 0
	for i < len(s.samples) && s.samples[i].at.Before(cutoff) {
		i++
	}
	if i > 0 {
		copy(s.samples, s.samples[i:])
		s.samples = s.samples[:len(s.samples)-i]
	}
}

func samplesSince(samples []daemonMetricSample, cutoff time.Time) []daemonMetricSample {
	out := make([]daemonMetricSample, 0, len(samples))
	for _, sample := range samples {
		if !sample.at.Before(cutoff) {
			out = append(out, sample)
		}
	}
	return out
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

func daemonMetricSeries(metric, unit string, since time.Duration, samples []daemonMetricSample, value func(daemonMetricSample) (float64, bool)) web.MetricSeries {
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
		Check:   "sermod",
		Metric:  metric,
		Since:   since.String(),
		Unit:    unit,
		Summary: daemonMetricSummary(summary),
		Points:  points,
	}
}

func addDaemonMetric(agg *daemonMetricAgg, value float64) {
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
	const maxInt64 = uint64(1<<63 - 1)
	if v > maxInt64 {
		return int64(maxInt64)
	}
	return int64(v)
}
