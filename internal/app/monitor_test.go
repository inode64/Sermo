package app

import (
	"errors"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/state"
)

// fakeStore is an in-memory MonitorStore for testing the startup reconciliation
// and the live pause check without a real database.
type fakeStore struct {
	active  map[string]bool
	source  map[string]string
	updated map[string]time.Time
	failGet bool
	failSet bool
	now     func() time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		active:  map[string]bool{},
		source:  map[string]string{},
		updated: map[string]time.Time{},
		now:     time.Now,
	}
}

func (f *fakeStore) Active(service string) (bool, bool, error) {
	if f.failGet {
		return false, false, errors.New("boom")
	}
	a, ok := f.active[service]
	return a, ok, nil
}

func (f *fakeStore) SetActive(service string, active bool, source string) error {
	if f.failSet {
		return errors.New("boom")
	}
	f.active[service] = active
	f.source[service] = source
	now := f.now
	if now == nil {
		now = time.Now
	}
	f.updated[service] = now()
	return nil
}

func (f *fakeStore) MonitorState(service string) (state.MonitorRecord, bool, error) {
	if f.failGet {
		return state.MonitorRecord{}, false, errors.New("boom")
	}
	a, ok := f.active[service]
	if !ok {
		return state.MonitorRecord{}, false, nil
	}
	return state.MonitorRecord{
		Active: a, Source: f.source[service], UpdatedAt: f.updated[service],
	}, true, nil
}

func TestApplyMonitorMode(t *testing.T) {
	cases := []struct {
		name       string
		mode       string
		seed       *bool // prior persisted state, nil = no row
		wantActive bool
		wantFound  bool
	}{
		{"enabled forces on", config.MonitorEnabled, boolPtr(false), true, true},
		{"disabled forces off", config.MonitorDisabled, boolPtr(true), false, true},
		{"previous keeps paused", config.MonitorPrevious, boolPtr(false), false, true},
		{"previous keeps active", config.MonitorPrevious, boolPtr(true), true, true},
		{"previous first run defaults on", config.MonitorPrevious, nil, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			if tc.seed != nil {
				store.active["svc"] = *tc.seed
			}
			if w := applyMonitorMode(store, "svc", tc.mode); w != "" {
				t.Fatalf("unexpected warning: %s", w)
			}
			active, found, _ := store.Active("svc")
			if found != tc.wantFound || active != tc.wantActive {
				t.Errorf("got found=%v active=%v, want found=%v active=%v", found, active, tc.wantFound, tc.wantActive)
			}
		})
	}
}

func TestApplyMonitorModeNilStore(t *testing.T) {
	if w := applyMonitorMode(nil, "svc", config.MonitorEnabled); w != "" {
		t.Errorf("nil store must be a no-op, got warning %q", w)
	}
}

func TestApplyMonitorModeReportsStoreError(t *testing.T) {
	store := newFakeStore()
	store.failSet = true
	if w := applyMonitorMode(store, "svc", config.MonitorEnabled); w == "" {
		t.Error("a store write failure must surface a warning")
	}
}

func TestMonitorPaused(t *testing.T) {
	store := newFakeStore()
	store.active["paused"] = false
	store.active["live"] = true

	if monitorPaused(nil, "x")() {
		t.Error("nil store must never report paused")
	}
	if !monitorPaused(store, "paused")() {
		t.Error("inactive service must report paused")
	}
	if monitorPaused(store, "live")() {
		t.Error("active service must not report paused")
	}
	// Unknown service and store errors both fail open (monitor, don't drop).
	if monitorPaused(store, "ghost")() {
		t.Error("unknown service must fail open (not paused)")
	}
	store.failGet = true
	if monitorPaused(store, "paused")() {
		t.Error("store error must fail open (not paused)")
	}
}

func boolPtr(b bool) *bool { return &b }
