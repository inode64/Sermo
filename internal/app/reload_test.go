package app

import (
	"path/filepath"
	"testing"
	"time"

	"sermo/internal/rules"
)

func TestCaptureAndApplyWorkerState(t *testing.T) {
	t0 := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	old := &Worker{
		Service: "web",
		cycle:   7,
		State: &rules.RemediationState{
			LastActionAt:   t0,
			RecentActions:  []time.Time{t0},
			CurrentBackoff: 2 * time.Minute,
		},
		windows: func() map[string]*rules.WindowState {
			r := rules.Rule{For: &rules.ForWindow{Cycles: 3}}
			ws := &rules.WindowState{}
			ws.FiresAt(r, true, time.Now())
			ws.FiresAt(r, true, time.Now())
			return map[string]*rules.WindowState{"restart-if-down": ws}
		}(),
		libBaseline:  testArtifactBaseline(map[string]string{"/etc/app.conf": "1:2"}),
		checkFailing: map[string]bool{"service": true},
	}
	saved := captureWorkerState([]*Worker{old})

	fresh := &Worker{Service: "web", State: &rules.RemediationState{}}
	applyWorkerState([]*Worker{fresh}, saved)

	if fresh.cycle != 7 {
		t.Fatalf("cycle = %d, want 7", fresh.cycle)
	}
	if !fresh.State.LastActionAt.Equal(t0) || fresh.State.CurrentBackoff != 2*time.Minute {
		t.Fatalf("remediation state = %+v", fresh.State)
	}
	if got := fresh.windows["restart-if-down"].Snapshot().Consecutive; got != 2 {
		t.Fatalf("window consecutive = %d, want 2", got)
	}
	if baselineFingerprints(fresh.libBaseline)["/etc/app.conf"] != "1:2" {
		t.Fatalf("baseline = %+v", baselineFingerprints(fresh.libBaseline))
	}
	if !fresh.checkFailing["service"] {
		t.Fatalf("check health state = %+v, want service failing", fresh.checkFailing)
	}
	fresh.checkFailing["service"] = false
	if !old.checkFailing["service"] {
		t.Fatal("applying worker state reused the old check-health map")
	}
}

// TestApplyWorkerStateKeepsEngineBaselineShared: the new generation's
// operation engine captured its worker's baseline at build time, so a reload
// must restore the acknowledged fingerprints into that same baseline.
func TestApplyWorkerStateKeepsEngineBaselineShared(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lib.so")
	writeFile(t, path, "upgraded")
	old := &Worker{Service: "web", libBaseline: testArtifactBaseline(map[string]string{path: "acknowledged-before-upgrade"})}
	saved := captureWorkerState([]*Worker{old})

	baseline := NewArtifactBaseline()
	engineChanged := ArtifactChangedFunc(baseline, nil)
	fresh := &Worker{Service: "web", libBaseline: baseline}
	applyWorkerState([]*Worker{fresh}, saved)

	if changed, err := engineChanged(path); err != nil || !changed {
		t.Fatalf("engine changed(%s) = %t, %v; want the pending change carried across the reload", path, changed, err)
	}
	fresh.acknowledgeChanges()
	if changed, err := engineChanged(path); err != nil || changed {
		t.Fatalf("engine changed(%s) after the worker acknowledged = %t, %v; want false", path, changed, err)
	}
}

// TestApplyWorkerStateKeepsAppVersionBaseline: a `changed: {app}` rule still
// waiting for its window, cooldown or guard when a reload happens must keep
// seeing the upgrade, like `changed: {path}` does.
func TestApplyWorkerStateKeepsAppVersionBaseline(t *testing.T) {
	old := &Worker{Service: "web", libBaseline: NewArtifactBaseline()}
	if changed, err := old.compareAppVersion("redis", 1, "redis 7.2.4"); err != nil || changed {
		t.Fatalf("first observation = %t, %v; want adopted", changed, err)
	}
	saved := captureWorkerState([]*Worker{old})

	fresh := &Worker{Service: "web", libBaseline: NewArtifactBaseline()}
	applyWorkerState([]*Worker{fresh}, saved)
	if changed, err := fresh.compareAppVersion("redis", 1, "redis 8.0.1"); err != nil || !changed {
		t.Fatalf("upgrade after reload = %t, %v; want the change against the preserved baseline", changed, err)
	}
}

func TestCaptureAndApplyWatchState(t *testing.T) {
	r := rules.Rule{For: &rules.ForWindow{Cycles: 3}}
	old := &Watch{
		Name:   "load-high",
		Window: r,
	}
	old.state.FiresAt(r, true, time.Now())
	old.state.FiresAt(r, true, time.Now())
	old.firing = true
	old.unavailable = true

	saved := captureWatchState([]*Watch{old})
	fresh := &Watch{Name: "load-high", Window: r}
	applyWatchState([]*Watch{fresh}, saved)

	if fresh.firing != true {
		t.Fatalf("firing = %v, want preserved", fresh.firing)
	}
	if !fresh.unavailable {
		t.Fatal("unavailable state was not preserved")
	}
	if got := fresh.state.Snapshot().Consecutive; got != 2 {
		t.Fatalf("window consecutive = %d, want 2", got)
	}
}

func TestCaptureAndApplyWatchStateKeepsMetricSlotsSeparate(t *testing.T) {
	r := rules.Rule{For: &rules.ForWindow{Cycles: 4}}
	rx := &Watch{Name: "uplink", StateSlot: "metric:rx", Window: r}
	tx := &Watch{Name: "uplink", StateSlot: "metric:tx", Window: r}
	rx.state.FiresAt(r, true, time.Now())
	tx.state.FiresAt(r, true, time.Now())
	tx.state.FiresAt(r, true, time.Now())

	saved := captureWatchState([]*Watch{rx, tx})
	freshTX := &Watch{Name: "uplink", StateSlot: "metric:tx", Window: r}
	freshRX := &Watch{Name: "uplink", StateSlot: "metric:rx", Window: r}
	applyWatchState([]*Watch{freshTX, freshRX}, saved)

	if got := freshRX.state.Snapshot().Consecutive; got != 1 {
		t.Fatalf("rx consecutive = %d, want 1", got)
	}
	if got := freshTX.state.Snapshot().Consecutive; got != 2 {
		t.Fatalf("tx consecutive = %d, want 2", got)
	}
}
