package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"sermo/internal/metrics"
	"sermo/internal/state"
	"sermo/internal/web"
)

type batchPersistentMetricStore struct {
	batches        int
	daemonMetrics  []string
	serviceMetrics []string
}

func (s *batchPersistentMetricStore) WithBatch(_ context.Context, record func(state.Batch) error) error {
	s.batches++
	return record(s)
}

func (*batchPersistentMetricStore) RecordSLA(string, bool, time.Time) error { return nil }
func (*batchPersistentMetricStore) RecordCheckSLA(string, string, bool, time.Time) error {
	return nil
}
func (*batchPersistentMetricStore) RecordMeasurement(string, string, float64, time.Time) error {
	return nil
}
func (*batchPersistentMetricStore) RecordMetric(string, string, string, float64, time.Time) error {
	return nil
}
func (s *batchPersistentMetricStore) RecordDaemonMetric(metric string, _ float64, _ time.Time) error {
	s.daemonMetrics = append(s.daemonMetrics, metric)
	return nil
}
func (*batchPersistentMetricStore) DaemonMetricSummary(string, time.Duration, time.Time) (state.MeasurementStat, error) {
	return state.MeasurementStat{}, nil
}
func (*batchPersistentMetricStore) DaemonMetricSeries(string, time.Time, time.Time) ([]state.MeasurementPoint, error) {
	return nil, nil
}
func (s *batchPersistentMetricStore) RecordServiceMetric(service, metric string, _ float64, _ time.Time) error {
	s.serviceMetrics = append(s.serviceMetrics, service+":"+metric)
	return nil
}
func (*batchPersistentMetricStore) ServiceMetricSummary(string, string, time.Duration, time.Time) (state.MeasurementStat, error) {
	return state.MeasurementStat{}, nil
}
func (*batchPersistentMetricStore) ServiceMetricSeries(string, string, time.Time, time.Time) ([]state.MeasurementPoint, error) {
	return nil, nil
}

func TestPersistentMetricSamplersUseBatch(t *testing.T) {
	at := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		record      func(*batchPersistentMetricStore)
		wantDaemon  string
		wantService string
	}{
		{
			name: "daemon metrics",
			record: func(store *batchPersistentMetricStore) {
				(&DaemonMetricSampler{store: store}).recordPersistent(context.Background(), daemonMetricSample{
					at: at, rss: 1024, rssOK: true, cpu: 20, cpuReady: true, io: 300, ioReady: true,
				})
			},
			wantDaemon: strings.Join([]string{metrics.MetricMemory, metrics.MetricCPU, metrics.MetricIO}, ","),
		},
		{
			name: "service runtime metrics",
			record: func(store *batchPersistentMetricStore) {
				NewServiceMetricSampler(store).recordPersistent(context.Background(), "web", web.ServiceRuntime{
					ProcessTotals: web.ProcessTotals{Count: 1, RSS: 1024, CPU: 20, HasCPU: true},
					IORate:        300,
					IOReady:       true,
				}, at)
			},
			wantService: strings.Join([]string{"web:" + metrics.MetricCPU, "web:" + metrics.MetricMemory, "web:" + metrics.MetricIO}, ","),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &batchPersistentMetricStore{}
			test.record(store)
			if store.batches != 1 {
				t.Fatalf("batches = %d, want 1", store.batches)
			}
			if got := strings.Join(store.daemonMetrics, ","); got != test.wantDaemon {
				t.Errorf("daemon metrics = %q, want %q", got, test.wantDaemon)
			}
			if got := strings.Join(store.serviceMetrics, ","); got != test.wantService {
				t.Errorf("service metrics = %q, want %q", got, test.wantService)
			}
		})
	}
}

func TestPersistentMetricSamplersSkipEmptyBatch(t *testing.T) {
	tests := []struct {
		name   string
		record func(*batchPersistentMetricStore)
	}{
		{
			name: "daemon metrics",
			record: func(store *batchPersistentMetricStore) {
				(&DaemonMetricSampler{store: store}).recordPersistent(context.Background(), daemonMetricSample{})
			},
		},
		{
			name: "service runtime metrics",
			record: func(store *batchPersistentMetricStore) {
				NewServiceMetricSampler(store).recordPersistent(context.Background(), "web", web.ServiceRuntime{}, time.Time{})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &batchPersistentMetricStore{}
			test.record(store)
			if store.batches != 0 {
				t.Fatalf("batches = %d, want 0", store.batches)
			}
		})
	}
}

func TestRecordPersistentMetricsContinuesAfterError(t *testing.T) {
	want := errors.New("CPU record failed")
	var recorded []string
	err := recordPersistentMetrics(func(metric string, _ float64, _ time.Time) error {
		recorded = append(recorded, metric)
		if metric == metrics.MetricCPU {
			return want
		}
		return nil
	}, time.Unix(0, 0), [3]persistentMetricValue{
		{name: metrics.MetricMemory, ready: true},
		{name: metrics.MetricCPU, ready: true},
		{name: metrics.MetricIO, ready: true},
	})
	if !errors.Is(err, want) {
		t.Fatalf("recordPersistentMetrics error = %v, want %v", err, want)
	}
	if got, want := strings.Join(recorded, ","), strings.Join([]string{metrics.MetricMemory, metrics.MetricCPU, metrics.MetricIO}, ","); got != want {
		t.Fatalf("recorded = %q, want %q", got, want)
	}
}
