package app

import (
	"cmp"
	"context"
	"fmt"
	"time"

	"sermo/internal/checks"
	"sermo/internal/rules"
	"sermo/internal/severity"
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
	w.unavailable = rec.Unavailable
	w.lastNotifyAt = rec.LastNotifyAt
	w.legacyNotified = rec.Firing && rec.NotifiedSeverity == "" && !rec.LastNotifyAt.IsZero()
	w.transitionHeard = severity.Level(rec.TransitionSeverity)
	w.state = *watchWindowStateFromRecord(rec)
	w.policyState = *remediationFromRecord(rec.Policy)
	w.persistedState = rec
	w.stateRestored = true
}

func (w *Watch) persistRuntimeState() {
	if w.StateStore == nil || !w.stateLoaded {
		return
	}
	rec := w.runtimeRecord()
	if !w.stateDirty && watchRuntimeRecordsEqual(rec, w.persistedState) {
		return
	}
	err := w.StateStore.SetWatchRuntimeState(w.runtimeStateName(), w.runtimeStateSlot(), rec)
	w.stateDirty = err != nil
	if err == nil {
		w.persistedState = rec
	}
	reportPersistenceResult(w.emit, Event{Watch: w.Name}, w.runtimeStateSlot(), "persist watch state", err)
}

func (w *Watch) runtimeRecord() state.WatchRuntimeRecord {
	rec := state.WatchRuntimeRecord{
		Firing:             w.firing,
		Unavailable:        w.unavailable,
		LastNotifyAt:       w.lastNotifyAt,
		Severity:           w.state.Severity().String(),
		NotifiedSeverity:   w.state.Notified().String(),
		TransitionSeverity: w.transitionHeard.String(),
		Policy:             remediationToRecord(&w.policyState),
	}
	if w.Window.For != nil || w.Window.Within != nil || w.Window.Clear != nil {
		rec.Window = ruleWindowRecord(&w.state).WindowRecord
	}
	return rec
}

func watchWindowStateFromRecord(rec state.WatchRuntimeRecord) *rules.WindowState {
	return windowStateFromRecord(state.RuleWindowRecord{
		WindowRecord: rec.Window, Firing: rec.Firing, Severity: rec.Severity, NotifiedSeverity: rec.NotifiedSeverity,
	})
}

// reconcileRestoredEpisode closes an episode the previous process left open
// when the first observation finds it no longer triggered, at the severity the
// episode had reached.
func (w *Watch) reconcileRestoredEpisode(ctx context.Context, res checks.Result) {
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
	// A record from before severity was kept has none: grade the recovery
	// by the result, as a fresh episode would have been.
	level := cmp.Or(w.state.Severity(), w.resultSeverity(res))
	w.adoptLegacyNotification(level)
	announced := w.state.Notified()
	w.firing = false
	w.state.EndEpisode()
	w.lastNotifyAt = time.Time{}
	w.emit(Event{Watch: w.Name, Kind: eventKindRecovered, Severity: level, Message: res.Message})
	w.notifyRecovery(ctx, res, announced)
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
	w.emit(Event{Watch: w.Name, Kind: eventKindError, Message: fmt.Sprintf("%s: %v", action, err)})
}

func watchRuntimeRecordsEqual(a, b state.WatchRuntimeRecord) bool {
	return a.Firing == b.Firing &&
		a.Unavailable == b.Unavailable &&
		a.LastNotifyAt.Equal(b.LastNotifyAt) &&
		a.Severity == b.Severity && a.NotifiedSeverity == b.NotifiedSeverity &&
		a.TransitionSeverity == b.TransitionSeverity &&
		windowRecordsEqual(a.Window, b.Window) &&
		remediationRecordsEqual(a.Policy, b.Policy)
}
