package app

import (
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/web"
)

func snapshotWatchConfig(device, label string) *config.Config {
	return cfgWithWatches(map[string]any{"disk": map[string]any{
		"display_name": label, "interval": "6h",
		"check": map[string]any{
			"type": "hdparm", "device": device,
			"read": map[string]any{"op": "<", "value": 20},
		},
	}})
}

func assignWatchConfigID(w *webWatch) string {
	if w.configID == "" {
		w.configID = watchSnapshotConfigID(map[string]any{
			config.WatchKeyCheck:  w.check,
			config.SectionMetrics: w.metrics,
		})
	}
	return w.configID
}

func publishWatchFor(s *WatchSnapshots, w *webWatch, result checks.Result) {
	s.publishConfigured(w.name, w.checkType, result, assignWatchConfigID(w))
}

const testServiceSnapshotConfigID = "test-service-config"

func stampServiceSnapshotConfig(s *Snapshots, service, configID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, snap := range s.byService[service] {
		if snap.ConfigID == "" {
			snap.ConfigID = configID
			s.byService[service][name] = snap
		}
	}
}

func bindServiceSnapshots(e *webEntry, s *Snapshots, service string) {
	if e.configID == "" {
		e.configID = testServiceSnapshotConfigID
	}
	stampServiceSnapshotConfig(s, service, e.configID)
}

func TestWatchSnapshotConfigSurvivesReloadAndRestart(t *testing.T) {
	for _, restart := range []bool{false, true} {
		for _, device := range []string{"/dev/sda", "/dev/sdb"} {
			t.Run(testName(restart, device), func(t *testing.T) {
				now := time.Unix(1_700_000_000, 0)
				store := &snapshotStoreFake{}
				samples, err := NewPersistentWatchSnapshots(store, nil)
				if err != nil {
					t.Fatal(err)
				}
				samples.now = func() time.Time { return now }
				deps := Deps{WatchSnapshots: samples, Now: func() time.Time { return now }}
				cfg := snapshotWatchConfig("/dev/sda", "old label")
				holder, warnings := NewWebBackendHolder(t.Context(), cfg, deps)
				if len(warnings) > 0 {
					t.Fatalf("NewWebBackendHolder: %v", warnings)
				}
				producers, warnings := BuildWatches(cfg, deps, time.Hour)
				if len(producers) != 1 || len(warnings) > 0 {
					t.Fatalf("producers=%d warnings=%v", len(producers), warnings)
				}
				result := checks.Result{Check: "disk", OK: true, Message: "old disk reading",
					Data: map[string]any{checks.DataKeyDevice: "/dev/sda", checks.HdparmFieldRead: 500.0}}
				producers[0].Publish("disk", checks.CheckTypeHdparm, result)
				assertSnapshotWatch(t, holder, true)
				next := snapshotWatchConfig(device, "new label")
				if restart {
					deps.WatchSnapshots, err = NewPersistentWatchSnapshots(store, nil)
					if err != nil {
						t.Fatal(err)
					}
					var restartWarnings []string
					holder, restartWarnings = NewWebBackendHolder(t.Context(), next, deps)
					if len(restartWarnings) > 0 {
						t.Fatalf("NewWebBackendHolder after restart: %v", restartWarnings)
					}
				} else {
					holder.Reload(t.Context(), next, deps)
				}
				assertSnapshotWatch(t, holder, device == "/dev/sda")
				// A producer pinned before reload can finish after the new backend
				// is installed. Its result must still carry the old identity.
				now = now.Add(time.Second)
				producers[0].Publish("disk", checks.CheckTypeHdparm, result)
				assertSnapshotWatch(t, holder, device == "/dev/sda")
			})
		}
	}
}

func testName(restart bool, device string) string {
	mode := "reload"
	if restart {
		mode = "restart"
	}
	return mode + device
}

func assertSnapshotWatch(t *testing.T, holder *WebBackendHolder, fresh bool) {
	t.Helper()
	ws := holder.Watches(t.Context())
	if len(ws) != 1 {
		t.Fatalf("watches=%+v", ws)
	}
	if fresh {
		if ws[0].SampleState != web.WatchSampleStateFresh || ws[0].Summary != "old disk reading" {
			t.Fatalf("unchanged check lost its reading: %+v", ws[0])
		}
		return
	}
	if ws[0].SampleState != web.WatchSampleStateCollecting || len(ws[0].Readings) != 0 || ws[0].LastCheckedAt != "" {
		t.Fatalf("changed check exposed old sample: %+v", ws[0])
	}
}

func TestSnapshotConfigIdentity(t *testing.T) {
	first := snapshotConfigID(map[string]any{"device": "/dev/sda", "type": "hdparm"})
	reordered := snapshotConfigID(map[string]any{"type": "hdparm", "device": "/dev/sda"})
	changed := snapshotConfigID(map[string]any{"type": "hdparm", "device": "/dev/sdb"})
	if first != reordered || first == changed {
		t.Fatal("snapshot identity must ignore map order and detect a different device")
	}
	invalid := snapshotConfigID(map[string]any{"unsupported": make(chan struct{})})
	if snapshotConfigMatches(invalid, invalid) || snapshotConfigMatches(first, "") {
		t.Fatal("invalid configuration and legacy unbound samples must not match")
	}
}

func TestServiceSnapshotRejectsChangedConfiguration(t *testing.T) {
	now := time.Now()
	samples := NewSnapshots()
	samples.now = func() time.Time { return now }
	types := map[string]string{"http": checks.CheckTypeHTTP}
	oldID := snapshotConfigID(map[string]any{"url": "http://old.invalid"})
	newID := snapshotConfigID(map[string]any{"url": "http://new.invalid"})
	publish := publishSnapshots(samples, "web", types, oldID)
	publish(map[string]checks.Result{"http": {OK: true}}, map[string]bool{"http": true})
	b := &WebBackend{now: func() time.Time { return now }}
	entry := &webEntry{checkTypes: types, configID: oldID, interval: time.Minute}
	snapshot := samples.Get("web")["http"]
	if !b.serviceCheckSnapshotCurrent(entry, "http", snapshot) {
		t.Fatal("current service configuration rejected its own sample")
	}
	entry.configID = newID
	if b.serviceCheckSnapshotCurrent(entry, "http", snapshot) {
		t.Fatal("changed service configuration accepted the previous endpoint's sample")
	}
	if got := checkFailingFromSnapshots(samples, "web", types, newID); got != nil {
		t.Fatalf("changed configuration restored old check health: %v", got)
	}
}
