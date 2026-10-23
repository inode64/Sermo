package app

import (
	"slices"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/state"
	"sermo/internal/telegrambot"
	"sermo/internal/web"
)

func TestTelegramReporterProjectsWebListings(t *testing.T) {
	backend := &WebBackend{
		cfg:   &config.Config{},
		order: []string{"database"},
		entries: map[string]*webEntry{
			"database": {disabled: true},
		},
		watchOrder: []string{"disk-space"},
		watches: map[string]*webWatch{
			"disk-space": {name: "disk-space", disabled: true},
		},
	}
	holder := &WebBackendHolder{}
	holder.current.Store(&webGeneration{backend: backend, generation: initialWebBackendGeneration})
	reporter := NewTelegramReporter(holder, nil, nil)

	services, err := reporter.Services(t.Context())
	if err != nil {
		t.Fatalf("Services() error = %v", err)
	}
	wantServices := []telegrambot.ServiceLine{{Name: "database", State: TargetStateDisabled}}
	if !slices.Equal(services, wantServices) {
		t.Errorf("Services() = %+v, want %+v", services, wantServices)
	}

	watches, err := reporter.Watches(t.Context())
	if err != nil {
		t.Fatalf("Watches() error = %v", err)
	}
	wantWatches := []telegrambot.WatchLine{{Name: "disk-space", Scope: web.WatchScopeHost, State: TargetStateDisabled}}
	if !slices.Equal(watches, wantWatches) {
		t.Errorf("Watches() = %+v, want %+v", watches, wantWatches)
	}
}

func TestTelegramReporterEmptyListings(t *testing.T) {
	reporter := NewTelegramReporter(&WebBackendHolder{}, nil, nil)
	services, serviceErr := reporter.Services(t.Context())
	watches, watchErr := reporter.Watches(t.Context())
	if serviceErr != nil || watchErr != nil {
		t.Fatalf("empty listings errors = (%v, %v)", serviceErr, watchErr)
	}
	if len(services) != 0 || len(watches) != 0 {
		t.Fatalf("empty listings = (%+v, %+v), want no entries", services, watches)
	}
}

func TestTelegramSLAFormatKeepsAffectedMinutes(t *testing.T) {
	if got, want := formatSLARatio(state.SLAValue{Up: 2, Total: 3, DownBuckets: 1}), "66.67% (1 min affected)"; got != want {
		t.Errorf("formatSLARatio() = %q, want %q", got, want)
	}
	if got, want := formatSLARatio(state.SLAValue{}), "n/a"; got != want {
		t.Errorf("formatSLARatio() = %q, want %q", got, want)
	}
}

type recordingSLAReader struct{ asked []string }

func (r *recordingSLAReader) SLAReport(service string, _ time.Time) ([]state.SLAValue, error) {
	r.asked = append(r.asked, service)
	return nil, nil
}

// `/sla MySQL` finds service mysql ignoring case, so the archive must be read
// under the configured name: its rows match the service column exactly.
func TestTelegramSLAReadsTheConfiguredServiceName(t *testing.T) {
	backend := &WebBackend{cfg: &config.Config{}, order: []string{"mysql"}, entries: map[string]*webEntry{"mysql": {}}}
	holder := &WebBackendHolder{}
	holder.current.Store(&webGeneration{backend: backend, generation: initialWebBackendGeneration})
	sla := &recordingSLAReader{}
	reporter := NewTelegramReporter(holder, sla, nil)

	if _, found, err := reporter.SLA(t.Context(), "MySQL"); err != nil || !found {
		t.Fatalf("SLA(MySQL) found=%v err=%v", found, err)
	}
	if _, found, _ := reporter.SLA(t.Context(), "postgres"); found {
		t.Fatal("SLA found a service that is not configured")
	}
	if !slices.Equal(sla.asked, []string{"mysql"}) {
		t.Fatalf("SLA archive read for %q, want only the configured name", sla.asked)
	}
}
