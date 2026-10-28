package state

import (
	"context"
	"math"
	"sync"
)

// pendingMinutes accumulates the samples of each series' still-open minute
// bucket in memory. Every cycle used to upsert the newest row of every series,
// and those rows live on a different page per series, so a 30 s cycle rewrote
// each of those pages twice a minute. Accumulated here, a bucket is written once
// when its minute has ended: by the first batch after the boundary, or by the
// consolidation pass; FlushPending writes the open one at a clean stop. Until
// then the per-minute reads
// of this process add it back, and a crash loses at most that open minute.
type pendingMinutes struct {
	mu      sync.Mutex
	metrics map[pendingMetricKey]metricAggregate
	sla     map[pendingSLAKey]slaAggregate
}

type pendingMetricKey struct {
	series metricSeries
	bucket int64
}

type pendingSLAKey struct {
	service string
	check   string
	bucket  int64
}

// metricAggregate is a bucket's sample count, sum and extremes.
type metricAggregate struct {
	n             int64
	sum, min, max float64
}

// metricSample is the aggregate of one observed value.
func metricSample(value float64) metricAggregate {
	return metricAggregate{n: 1, sum: value, min: value, max: value}
}

// plus combines two aggregates; an empty one (a missing map entry) has no
// extremes to compare.
func (a metricAggregate) plus(b metricAggregate) metricAggregate {
	switch {
	case a.n == 0:
		return b
	case b.n == 0:
		return a
	}
	return metricAggregate{n: a.n + b.n, sum: a.sum + b.sum, min: math.Min(a.min, b.min), max: math.Max(a.max, b.max)}
}

// slaAggregate is a bucket's up and observed cycle counts.
type slaAggregate struct {
	up, total int64
}

// slaCycle is the aggregate of one observed cycle.
func slaCycle(up bool) slaAggregate {
	return slaAggregate{up: int64(boolInt(up)), total: 1}
}

func (a slaAggregate) plus(b slaAggregate) slaAggregate {
	return slaAggregate{up: a.up + b.up, total: a.total + b.total}
}

// down reports whether any cycle in the minute failed: at per-minute resolution
// the bucket is the unit down_buckets counts.
func (a slaAggregate) down() int64 {
	if a.total > a.up {
		return 1
	}
	return 0
}

func (p *pendingMinutes) addMetric(m metricSeries, bucket int64, agg metricAggregate) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.metrics == nil {
		p.metrics = map[pendingMetricKey]metricAggregate{}
	}
	key := pendingMetricKey{series: m, bucket: bucket}
	p.metrics[key] = p.metrics[key].plus(agg)
}

func (p *pendingMinutes) addSLA(service, check string, bucket int64, agg slaAggregate) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sla == nil {
		p.sla = map[pendingSLAKey]slaAggregate{}
	}
	key := pendingSLAKey{service: service, check: check, bucket: bucket}
	p.sla[key] = p.sla[key].plus(agg)
}

// empty reports whether nothing is pending, so a reader-only process never
// opens a write transaction to flush.
func (p *pendingMinutes) empty() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.metrics) == 0 && len(p.sla) == 0
}

// take removes and returns every bucket that starts before cutoff.
func (p *pendingMinutes) take(cutoff int64) (map[pendingMetricKey]metricAggregate, map[pendingSLAKey]slaAggregate) {
	p.mu.Lock()
	defer p.mu.Unlock()
	metrics := map[pendingMetricKey]metricAggregate{}
	for key, agg := range p.metrics {
		if key.bucket < cutoff {
			metrics[key] = agg
			delete(p.metrics, key)
		}
	}
	sla := map[pendingSLAKey]slaAggregate{}
	for key, agg := range p.sla {
		if key.bucket < cutoff {
			sla[key] = agg
			delete(p.sla, key)
		}
	}
	return metrics, sla
}

// merge adds buckets to the pending ones: the samples a committed batch staged,
// or the buckets a failed write took back.
func (p *pendingMinutes) merge(metrics map[pendingMetricKey]metricAggregate, sla map[pendingSLAKey]slaAggregate) {
	if len(metrics) == 0 && len(sla) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.metrics == nil {
		p.metrics = map[pendingMetricKey]metricAggregate{}
	}
	for key, agg := range metrics {
		p.metrics[key] = agg.plus(p.metrics[key])
	}
	if p.sla == nil {
		p.sla = map[pendingSLAKey]slaAggregate{}
	}
	for key, agg := range sla {
		p.sla[key] = agg.plus(p.sla[key])
	}
}

// metricBuckets returns the series' pending buckets in [from, to).
func (p *pendingMinutes) metricBuckets(m metricSeries, from, to int64) map[int64]metricAggregate {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out map[int64]metricAggregate
	for key, agg := range p.metrics {
		if key.series == m && key.bucket >= from && key.bucket < to {
			if out == nil {
				out = map[int64]metricAggregate{}
			}
			out[key.bucket] = agg
		}
	}
	return out
}

// slaBuckets returns the series' pending buckets in [from, to).
func (p *pendingMinutes) slaBuckets(service, check string, from, to int64) map[int64]slaAggregate {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out map[int64]slaAggregate
	for key, agg := range p.sla {
		if key.service == service && key.check == check && key.bucket >= from && key.bucket < to {
			if out == nil {
				out = map[int64]slaAggregate{}
			}
			out[key.bucket] = agg
		}
	}
	return out
}

// writePending stores taken buckets through exec, adding to any row already
// stored for the same bucket.
func writePending(ctx context.Context, exec statementExecutor, metrics map[pendingMetricKey]metricAggregate, sla map[pendingSLAKey]slaAggregate) error {
	for key, agg := range metrics {
		if err := writeMetricBucket(ctx, exec, key.series, key.bucket, agg); err != nil {
			return err
		}
	}
	for key, agg := range sla {
		if err := writeSLABucket(ctx, exec, key.service, key.check, key.bucket, agg); err != nil {
			return err
		}
	}
	return nil
}
