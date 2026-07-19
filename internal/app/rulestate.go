package app

import (
	"fmt"
	"maps"
	"slices"
	"time"

	"sermo/internal/rules"
	"sermo/internal/state"
)

func loadRuleState(store RuleStateStore, service string, ruleSet []rules.Rule) (*rules.RemediationState, map[string]*rules.WindowState, []string) {
	remediation := &rules.RemediationState{}
	if store == nil {
		return remediation, nil, nil
	}

	var warnings []string
	if rec, found, err := store.RemediationState(service); err != nil {
		warnings = append(warnings, "load remediation state: "+err.Error())
	} else if found {
		remediation = remediationFromRecord(rec)
	}

	records, err := store.RuleWindowStates(service)
	if err != nil {
		warnings = append(warnings, "load rule window state: "+err.Error())
		return remediation, nil, warnings
	}
	plan := newRuleStatePlan(ruleSet)
	windows := make(map[string]*rules.WindowState, len(records))
	for name, rec := range records {
		if !plan.tracks(name) {
			continue
		}
		windows[name] = windowStateFromRecord(rec)
	}
	if len(windows) == 0 {
		windows = nil
	}
	return remediation, windows, warnings
}

func ruleStatePersister(store RuleStateStore, emit func(Event), service string, ruleSet []rules.Rule) func(*rules.RemediationState, map[string]*rules.WindowState) {
	if store == nil {
		return nil
	}
	plan := newRuleStatePlan(ruleSet)
	// Change-gated like the watch runtime state: a steady cycle whose rule
	// state matches the last successful persist writes nothing. The closure
	// runs only on its service's worker goroutine, so no locking is needed.
	var lastPrimed bool
	var lastRemediation state.RemediationRecord
	var lastWindows map[string]state.RuleWindowRecord
	return func(remediation *rules.RemediationState, windows map[string]*rules.WindowState) {
		remediationRecord := state.RemediationRecord{}
		if plan.hasRemediation {
			remediationRecord = remediationToRecord(remediation)
		}
		records := map[string]state.RuleWindowRecord{}
		for name, window := range windows {
			if !plan.tracks(name) || window == nil {
				continue
			}
			records[name] = ruleWindowRecord(window)
		}
		if lastPrimed && remediationRecordsEqual(remediationRecord, lastRemediation) && ruleWindowMapsEqual(records, lastWindows) {
			return
		}

		persisted := true
		context := "persist remediation state"
		if !plan.hasRemediation {
			context = "delete remediation state"
		}
		if err := store.SetRemediationState(service, remediationRecord); err != nil {
			emitRuleStateError(emit, service, context, err)
			persisted = false
		}
		if err := store.SetRuleWindowStates(service, records); err != nil {
			emitRuleStateError(emit, service, "persist rule window state", err)
			persisted = false
		}
		if persisted {
			lastPrimed, lastRemediation, lastWindows = true, remediationRecord, records
		}
	}
}

// remediationRecordsEqual compares records with time.Equal so monotonic-clock
// noise never defeats the change gate.
func remediationRecordsEqual(a, b state.RemediationRecord) bool {
	return a.LastActionAt.Equal(b.LastActionAt) && a.CurrentBackoff == b.CurrentBackoff &&
		slices.EqualFunc(a.RecentActions, b.RecentActions, time.Time.Equal)
}

func ruleWindowRecordsEqual(a, b state.RuleWindowRecord) bool {
	return a.Consecutive == b.Consecutive && a.Firing == b.Firing && a.ClearConsecutive == b.ClearConsecutive &&
		a.TrueSince.Equal(b.TrueSince) && a.ClearSince.Equal(b.ClearSince) &&
		slices.Equal(a.History, b.History) &&
		slices.EqualFunc(a.TimedHistory, b.TimedHistory, func(x, y state.RuleWindowSample) bool {
			return x.Match == y.Match && x.At.Equal(y.At)
		})
}

func ruleWindowMapsEqual(a, b map[string]state.RuleWindowRecord) bool {
	return maps.EqualFunc(a, b, ruleWindowRecordsEqual)
}

type ruleStatePlan struct {
	names          map[string]bool
	hasRemediation bool
}

func newRuleStatePlan(ruleSet []rules.Rule) ruleStatePlan {
	plan := ruleStatePlan{names: make(map[string]bool, len(ruleSet))}
	for i := range ruleSet {
		switch ruleSet[i].Type {
		case rules.RuleRemediation:
			plan.names[ruleSet[i].Name] = true
			plan.hasRemediation = true
		case rules.RuleAlert:
			plan.names[ruleSet[i].Name] = true
		default: // guard rules keep no persisted window state
		}
	}
	return plan
}

func (p ruleStatePlan) tracks(name string) bool {
	return p.names[name]
}

func remediationFromRecord(rec state.RemediationRecord) *rules.RemediationState {
	return &rules.RemediationState{
		LastActionAt:   rec.LastActionAt,
		RecentActions:  append([]time.Time(nil), rec.RecentActions...),
		CurrentBackoff: rec.CurrentBackoff,
	}
}

func remediationToRecord(remediation *rules.RemediationState) state.RemediationRecord {
	if remediation == nil {
		return state.RemediationRecord{}
	}
	return state.RemediationRecord{
		LastActionAt:   remediation.LastActionAt,
		RecentActions:  append([]time.Time(nil), remediation.RecentActions...),
		CurrentBackoff: remediation.CurrentBackoff,
	}
}

func windowStateFromRecord(rec state.RuleWindowRecord) *rules.WindowState {
	return rules.WindowStateFromSnapshot(rules.WindowStateSnapshot{
		Consecutive:      rec.Consecutive,
		History:          append([]bool(nil), rec.History...),
		TrueSince:        rec.TrueSince,
		TimedHistory:     ruleSamplesFromRecords(rec.TimedHistory),
		Firing:           rec.Firing,
		ClearConsecutive: rec.ClearConsecutive,
		ClearSince:       rec.ClearSince,
	})
}

func ruleWindowRecord(window *rules.WindowState) state.RuleWindowRecord {
	snapshot := window.Snapshot()
	return state.RuleWindowRecord{
		Consecutive:      snapshot.Consecutive,
		History:          append([]bool(nil), snapshot.History...),
		TrueSince:        snapshot.TrueSince,
		TimedHistory:     ruleRecordsFromSamples(snapshot.TimedHistory),
		Firing:           snapshot.Firing,
		ClearConsecutive: snapshot.ClearConsecutive,
		ClearSince:       snapshot.ClearSince,
	}
}

func ruleSamplesFromRecords(records []state.RuleWindowSample) []rules.WindowSample {
	return mapSlice(records, func(rec state.RuleWindowSample) rules.WindowSample {
		return rules.WindowSample{At: rec.At, Match: rec.Match}
	})
}

func ruleRecordsFromSamples(samples []rules.WindowSample) []state.RuleWindowSample {
	return mapSlice(samples, func(sample rules.WindowSample) state.RuleWindowSample {
		return state.RuleWindowSample{At: sample.At, Match: sample.Match}
	})
}

func emitRuleStateError(emit func(Event), service, action string, err error) {
	if emit != nil && err != nil {
		emit(Event{Service: service, Kind: eventKindError, Message: fmt.Sprintf("%s: %v", action, err)})
	}
}
