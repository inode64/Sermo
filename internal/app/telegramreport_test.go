package app

import (
	"slices"
	"testing"

	"sermo/internal/config"
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
