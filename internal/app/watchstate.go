package app

import (
	"fmt"
	"slices"
	"time"

	"sermo/internal/checks"
	"sermo/internal/state"
)

const (
	watchStateDefaultSlot = checks.DataKeyResult
	watchStateAppPrefix   = "app:"
)

func (w *Watch) loadRuntimeState() {
	if w.StateStore == nil || w.stateLoaded {
		return
	}
	w.stateLoaded = true
	rec, found, err := w.StateStore.WatchRuntimeState(w.runtimeStateName(), w.runtimeStateSlot())
	if err != nil {
		w.emitWatchStateError("load watch state", err)
		return
	}
	if !found {
		return
	}
	w.firing = rec.Firing
	w.lastNotifyAt = rec.LastNotifyAt
	w.state = *windowStateFromRecord(rec.Window)
	if policy := remediationFromRecord(rec.Policy); policy != nil {
		w.policyState = *policy
	}
	w.persistedState = cloneWatchRuntimeRecord(rec)
	w.stateRestored = true
}

func (w *Watch) persistRuntimeState() {
	if w.StateStore == nil || !w.stateLoaded {
		return
	}
	rec := w.runtimeRecord()
	if watchRuntimeRecordsEqual(rec, w.persistedState) {
		return
	}
	if err := w.StateStore.SetWatchRuntimeState(w.runtimeStateName(), w.runtimeStateSlot(), rec); err != nil {
		w.emitWatchStateError("persist watch state", err)
		return
	}
	w.persistedState = cloneWatchRuntimeRecord(rec)
}

func (w *Watch) runtimeRecord() state.WatchRuntimeRecord {
	rec := state.WatchRuntimeRecord{
		Firing:       w.firing,
		LastNotifyAt: w.lastNotifyAt,
		Policy:       remediationToRecord(&w.policyState),
	}
	if w.Window.For != nil || w.Window.Within != nil || w.Window.Clear != nil {
		rec.Window = ruleWindowRecord(&w.state)
	}
	return rec
}

func (w *Watch) reconcileRestoredEpisode(res checks.Result) {
	if !w.stateRestored || !w.firing {
		return
	}
	triggered := res.OK
	if w.FireOnFail {
		triggered = !res.OK
	}
	if triggered {
		return
	}
	w.firing = false
	w.state.EndEpisode()
	w.lastNotifyAt = time.Time{}
	w.emit(Event{Watch: w.Name, Kind: eventKindRecovered, Message: res.Message})
}

func (w *Watch) runtimeStateName() string {
	if w.App != "" {
		return watchStateAppPrefix + w.App
	}
	return w.Name
}

func (w *Watch) runtimeStateSlot() string {
	if w.StateSlot != "" {
		return w.StateSlot
	}
	return watchStateDefaultSlot
}

func (w *Watch) emitWatchStateError(action string, err error) {
	if err != nil {
		w.emit(Event{Watch: w.Name, Kind: eventKindError, Message: fmt.Sprintf("%s: %v", action, err)})
	}
}

func cloneWatchRuntimeRecord(rec state.WatchRuntimeRecord) state.WatchRuntimeRecord {
	rec.Window.History = slices.Clone(rec.Window.History)
	rec.Window.TimedHistory = slices.Clone(rec.Window.TimedHistory)
	rec.Policy.RecentActions = slices.Clone(rec.Policy.RecentActions)
	return rec
}

func watchRuntimeRecordsEqual(a, b state.WatchRuntimeRecord) bool {
	return a.Firing == b.Firing &&
		a.LastNotifyAt.Equal(b.LastNotifyAt) &&
		a.Window.Consecutive == b.Window.Consecutive &&
		slices.Equal(a.Window.History, b.Window.History) &&
		a.Window.TrueSince.Equal(b.Window.TrueSince) &&
		windowSamplesEqual(a.Window.TimedHistory, b.Window.TimedHistory) &&
		a.Window.Firing == b.Window.Firing &&
		a.Window.ClearSince.Equal(b.Window.ClearSince) &&
		a.Window.ClearConsecutive == b.Window.ClearConsecutive &&
		a.Policy.LastActionAt.Equal(b.Policy.LastActionAt) &&
		slices.EqualFunc(a.Policy.RecentActions, b.Policy.RecentActions, func(x, y time.Time) bool { return x.Equal(y) }) &&
		a.Policy.CurrentBackoff == b.Policy.CurrentBackoff
}

func windowSamplesEqual(a, b []state.RuleWindowSample) bool {
	return slices.EqualFunc(a, b, func(x, y state.RuleWindowSample) bool {
		return x.Match == y.Match && x.At.Equal(y.At)
	})
}
