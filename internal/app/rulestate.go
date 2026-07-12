package app

import (
	"fmt"
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
	return func(remediation *rules.RemediationState, windows map[string]*rules.WindowState) {
		if plan.hasRemediation {
			if err := store.SetRemediationState(service, remediationToRecord(remediation)); err != nil {
				emitRuleStateError(emit, service, "persist remediation state", err)
			}
		} else if err := store.SetRemediationState(service, state.RemediationRecord{}); err != nil {
			emitRuleStateError(emit, service, "delete remediation state", err)
		}

		records := map[string]state.RuleWindowRecord{}
		for name, window := range windows {
			if !plan.tracks(name) || window == nil {
				continue
			}
			records[name] = ruleWindowRecord(window)
		}
		if err := store.SetRuleWindowStates(service, records); err != nil {
			emitRuleStateError(emit, service, "persist rule window state", err)
		}
	}
}

type ruleStatePlan struct {
	names          map[string]bool
	hasRemediation bool
}

func newRuleStatePlan(ruleSet []rules.Rule) ruleStatePlan {
	plan := ruleStatePlan{names: make(map[string]bool, len(ruleSet))}
	for _, r := range ruleSet {
		switch r.Type {
		case rules.RuleRemediation:
			plan.names[r.Name] = true
			plan.hasRemediation = true
		case rules.RuleAlert:
			plan.names[r.Name] = true
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
		Consecutive:  rec.Consecutive,
		History:      append([]bool(nil), rec.History...),
		TrueSince:    rec.TrueSince,
		TimedHistory: ruleSamplesFromRecords(rec.TimedHistory),
	})
}

func ruleWindowRecord(window *rules.WindowState) state.RuleWindowRecord {
	snapshot := window.Snapshot()
	return state.RuleWindowRecord{
		Consecutive:  snapshot.Consecutive,
		History:      append([]bool(nil), snapshot.History...),
		TrueSince:    snapshot.TrueSince,
		TimedHistory: ruleRecordsFromSamples(snapshot.TimedHistory),
	}
}

func ruleSamplesFromRecords(records []state.RuleWindowSample) []rules.WindowSample {
	if len(records) == 0 {
		return nil
	}
	out := make([]rules.WindowSample, 0, len(records))
	for _, rec := range records {
		out = append(out, rules.WindowSample{At: rec.At, Match: rec.Match})
	}
	return out
}

func ruleRecordsFromSamples(samples []rules.WindowSample) []state.RuleWindowSample {
	if len(samples) == 0 {
		return nil
	}
	out := make([]state.RuleWindowSample, 0, len(samples))
	for _, sample := range samples {
		out = append(out, state.RuleWindowSample{At: sample.At, Match: sample.Match})
	}
	return out
}

func emitRuleStateError(emit func(Event), service, action string, err error) {
	if emit != nil && err != nil {
		emit(Event{Service: service, Kind: eventKindError, Message: fmt.Sprintf("%s: %v", action, err)})
	}
}
