package app

import (
	"testing"
	"time"

	"sermo/internal/metrics"
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
			ws.Fires(r, true)
			ws.Fires(r, true)
			return map[string]*rules.WindowState{"restart-if-down": ws}
		}(),
		libBaseline: map[string]string{"/etc/app.conf": "1:2"},
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
	r := rules.Rule{For: &rules.ForWindow{Cycles: 3}}
	if fresh.windows["restart-if-down"].Progress(r) != "2/3" {
		t.Fatalf("windows = %+v", fresh.windows["restart-if-down"].Progress(r))
	}
	if fresh.libBaseline["/etc/app.conf"] != "1:2" {
		t.Fatalf("baseline = %+v", fresh.libBaseline)
	}
}

func TestCaptureAndApplyWatchState(t *testing.T) {
	r := rules.Rule{For: &rules.ForWindow{Cycles: 3}}
	old := &Watch{
		Name:   "load-high",
		Window: r,
	}
	old.state.Fires(r, true)
	old.state.Fires(r, true)
	old.firing = true

	saved := captureWatchState([]*Watch{old})
	fresh := &Watch{Name: "load-high", Window: r}
	applyWatchState([]*Watch{fresh}, saved)

	if fresh.firing != true {
		t.Fatalf("firing = %v, want preserved", fresh.firing)
	}
	if fresh.state.Progress(r) != "2/3" {
		t.Fatalf("window progress = %q, want 2/3", fresh.state.Progress(r))
	}
}

func TestCaptureAndApplyWatchStateKeepsMetricSlotsSeparate(t *testing.T) {
	r := rules.Rule{For: &rules.ForWindow{Cycles: 4}}
	rx := &Watch{Name: "uplink", StateSlot: "metric:rx", Window: r}
	tx := &Watch{Name: "uplink", StateSlot: "metric:tx", Window: r}
	rx.state.Fires(r, true)
	tx.state.Fires(r, true)
	tx.state.Fires(r, true)

	saved := captureWatchState([]*Watch{rx, tx})
	freshTX := &Watch{Name: "uplink", StateSlot: "metric:tx", Window: r}
	freshRX := &Watch{Name: "uplink", StateSlot: "metric:rx", Window: r}
	applyWatchState([]*Watch{freshTX, freshRX}, saved)

	if got := freshRX.state.Progress(r); got != "1/4" {
		t.Fatalf("rx progress = %q, want 1/4", got)
	}
	if got := freshTX.state.Progress(r); got != "2/4" {
		t.Fatalf("tx progress = %q, want 2/4", got)
	}
}

func TestResetRemovedServiceMetrics(t *testing.T) {
	collector := metrics.New(metrics.OSReader{})
	resetRemovedServiceMetrics(collector,
		[]*Worker{{Service: "gone"}, {Service: "stay"}},
		[]*Worker{{Service: "stay"}, {Service: "new"}},
	)
	// No panic and no observable API beyond Reset; smoke test only.
}
