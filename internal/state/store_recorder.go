package state

import (
	"context"
	"time"
)

// recorder writes observations through one statement executor: the Store's
// pooled prepared statements or a batch's transaction-local ones. Store and
// batch embed it, so every Record* method exists once and a batch records
// exactly what the store records.
type recorder struct {
	ctx  func() context.Context
	exec statementExecutor
}

// RecordSLA accumulates one observed monitoring cycle into a service's current
// UTC-minute bucket: total_count +1, and up_count +1 when up. Paused or
// unobserved cycles are simply never recorded, so they do not count as downtime.
func (r recorder) RecordSLA(service string, up bool, at time.Time) error {
	return recordSLABucket(r.ctx(), r.exec, service, "", up, at)
}

// RecordCheckSLA accumulates one observed check execution into its current
// UTC-minute bucket. Interval-deferred checks are not recorded by callers, so
// the per-check SLA reflects only real check runs.
func (r recorder) RecordCheckSLA(service, check string, up bool, at time.Time) error {
	return recordSLABucket(r.ctx(), r.exec, service, check, up, at)
}

// RecordMeasurement accumulates one numeric observation (milliseconds) for a
// service+check into its current UTC-minute bucket.
func (r recorder) RecordMeasurement(service, check string, valueMs float64, at time.Time) error {
	return r.record(checkLatencySeries(service, check), valueMs, at)
}

// RecordMetric accumulates one observation of a named per-check metric (e.g.
// hdparm "read" MB/s) into its current UTC-minute bucket. It is the generic
// counterpart of RecordMeasurement (latency).
func (r recorder) RecordMetric(service, check, metric string, value float64, at time.Time) error {
	return r.record(checkMetricSeries(service, check, metric), value, at)
}

// RecordDaemonMetric accumulates one sermod process metric observation into its
// current UTC-minute bucket.
func (r recorder) RecordDaemonMetric(metric string, value float64, at time.Time) error {
	return r.record(daemonRuntimeSeries(metric), value, at)
}

// RecordServiceMetric accumulates one service process-tree metric observation
// into its current UTC-minute bucket.
func (r recorder) RecordServiceMetric(service, metric string, value float64, at time.Time) error {
	return r.record(serviceRuntimeSeries(service, metric), value, at)
}

// record accumulates one observation into the series' current per-minute bucket:
// n+1, sum+value and the running min/max.
func (r recorder) record(m metricSeries, value float64, at time.Time) error {
	return recordMetric(r.ctx(), r.exec, m, value, at)
}
