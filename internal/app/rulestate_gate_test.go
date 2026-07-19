package app

import (
	"testing"
	"time"

	"sermo/internal/rules"
	"sermo/internal/state"
)

type countingRuleStateStore struct {
	remediationSets int
	windowSets      int
}

func (c *countingRuleStateStore) RemediationState(string) (state.RemediationRecord, bool, error) {
	return state.RemediationRecord{}, false, nil
}

func (c *countingRuleStateStore) SetRemediationState(string, state.RemediationRecord) error {
	c.remediationSets++
	return nil
}

func (c *countingRuleStateStore) RuleWindowStates(string) (map[string]state.RuleWindowRecord, error) {
	return map[string]state.RuleWindowRecord{}, nil
}

func (c *countingRuleStateStore) SetRuleWindowStates(string, map[string]state.RuleWindowRecord) error {
	c.windowSets++
	return nil
}

// A steady cycle whose rule state matches the last successful persist must
// write nothing; a change resumes writing.
func TestRuleStatePersisterSkipsUnchangedState(t *testing.T) {
	store := &countingRuleStateStore{}
	ruleSet := []rules.Rule{{Name: "restart-on-fail", Type: rules.RuleRemediation}}
	persist := ruleStatePersister(store, nil, "web", ruleSet)

	at := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	remediation := &rules.RemediationState{}
	windows := map[string]*rules.WindowState{
		"restart-on-fail": windowStateFromRecord(state.RuleWindowRecord{Consecutive: 2, TrueSince: at}),
	}

	persist(remediation, windows)
	if store.remediationSets != 1 || store.windowSets != 1 {
		t.Fatalf("first persist wrote %d/%d, want 1/1", store.remediationSets, store.windowSets)
	}
	persist(remediation, windows)
	if store.remediationSets != 1 || store.windowSets != 1 {
		t.Fatalf("unchanged persist wrote %d/%d, want still 1/1", store.remediationSets, store.windowSets)
	}
	windows["restart-on-fail"] = windowStateFromRecord(state.RuleWindowRecord{Consecutive: 3, TrueSince: at})
	persist(remediation, windows)
	if store.remediationSets != 2 || store.windowSets != 2 {
		t.Fatalf("changed persist wrote %d/%d, want 2/2", store.remediationSets, store.windowSets)
	}
}
