package app

import (
	"maps"
	"slices"
	"time"

	"sermo/internal/metrics"
	"sermo/internal/rules"
	"sermo/internal/state"
)

// watchSnapshot preserves per-watch window and policy pacing state across reload.
type watchSnapshot struct {
	state          rules.WindowState
	policyState    *rules.RemediationState
	firing         bool
	lastNotifyAt   time.Time
	settled        bool
	stateLoaded    bool
	stateRestored  bool
	persistedState state.WatchRuntimeRecord
}

type watchStateKey struct {
	name string
	slot string
}

func captureWatchState(watches []*Watch) map[watchStateKey]watchSnapshot {
	out := make(map[watchStateKey]watchSnapshot, len(watches))
	for _, w := range watches {
		if w == nil {
			continue
		}
		snap := watchSnapshot{
			firing:         w.firing,
			lastNotifyAt:   w.lastNotifyAt,
			settled:        w.settled,
			stateLoaded:    w.stateLoaded,
			stateRestored:  w.stateRestored,
			persistedState: cloneWatchRuntimeRecord(w.persistedState),
			policyState:    cloneRemediationState(&w.policyState),
		}
		if cloned := w.state.Clone(); cloned != nil {
			snap.state = *cloned
		}
		out[watchStateKey{name: w.runtimeStateName(), slot: w.runtimeStateSlot()}] = snap
	}
	return out
}

func applyWatchState(watches []*Watch, saved map[watchStateKey]watchSnapshot) {
	for _, w := range watches {
		snap, ok := saved[watchStateKey{name: w.runtimeStateName(), slot: w.runtimeStateSlot()}]
		if !ok {
			continue
		}
		w.state = snap.state
		w.firing = snap.firing
		w.lastNotifyAt = snap.lastNotifyAt
		w.settled = snap.settled
		w.stateLoaded = snap.stateLoaded
		w.stateRestored = snap.stateRestored
		w.persistedState = cloneWatchRuntimeRecord(snap.persistedState)
		if snap.policyState != nil {
			w.policyState = *snap.policyState
		}
	}
}

// workerSnapshot preserves per-service runtime state across a config reload.
type workerSnapshot struct {
	cycle       int
	remediation *rules.RemediationState
	windows     map[string]*rules.WindowState
	libBaseline map[string]string
}

func captureWorkerState(workers []*Worker) map[string]workerSnapshot {
	out := make(map[string]workerSnapshot, len(workers))
	for _, w := range workers {
		if w == nil {
			continue
		}
		snap := workerSnapshot{cycle: w.cycle}
		if w.State != nil {
			snap.remediation = cloneRemediationState(w.State)
		}
		if len(w.windows) > 0 {
			snap.windows = cloneWindowStates(w.windows)
		}
		if len(w.libBaseline) > 0 {
			snap.libBaseline = maps.Clone(w.libBaseline)
		}
		out[w.Service] = snap
	}
	return out
}

func applyWorkerState(workers []*Worker, saved map[string]workerSnapshot) {
	for _, w := range workers {
		snap, ok := saved[w.Service]
		if !ok {
			continue
		}
		w.cycle = snap.cycle
		if snap.remediation != nil {
			w.State = snap.remediation
		}
		if snap.windows != nil {
			w.windows = snap.windows
		}
		if snap.libBaseline != nil {
			w.libBaseline = snap.libBaseline
		}
	}
}

func resetRemovedServiceMetrics(collector *metrics.Collector, oldWorkers, newWorkers []*Worker) {
	if collector == nil {
		return
	}
	oldNames := workerServiceNames(oldWorkers)
	newNames := workerServiceNames(newWorkers)
	for name := range oldNames {
		if !newNames[name] {
			collector.Reset(name)
		}
	}
}

func workerServiceNames(workers []*Worker) map[string]bool {
	names := make(map[string]bool, len(workers))
	for _, w := range workers {
		if w != nil {
			names[w.Service] = true
		}
	}
	return names
}

func cloneRemediationState(s *rules.RemediationState) *rules.RemediationState {
	if s == nil {
		return nil
	}
	out := *s
	if len(s.RecentActions) > 0 {
		out.RecentActions = slices.Clone(s.RecentActions)
	}
	return &out
}

func cloneWindowStates(in map[string]*rules.WindowState) map[string]*rules.WindowState {
	out := make(map[string]*rules.WindowState, len(in))
	for name, ws := range in {
		out[name] = ws.Clone()
	}
	return out
}
