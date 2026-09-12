package app

import (
	"testing"
	"time"

	"sermo/internal/rules"
	"sermo/internal/state"
)

func TestWindowRecordCopiesIsolateBothDirections(t *testing.T) {
	at := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	record := state.RuleWindowRecord{History: []bool{true}, TimedHistory: []rules.WindowSample{{At: at}}}
	window := windowStateFromRecord(record)
	record.History[0] = false
	record.TimedHistory[0].At = time.Time{}
	published := ruleWindowRecord(window)
	if !published.History[0] || !published.TimedHistory[0].At.Equal(at) {
		t.Fatal("restored window aliases input record")
	}
	published.History[0] = false
	published.TimedHistory[0].At = time.Time{}
	again := ruleWindowRecord(window)
	if !again.History[0] || !again.TimedHistory[0].At.Equal(at) {
		t.Fatal("published record aliases live window")
	}
}

func TestWindowRecordNormalizesEmptyHistories(t *testing.T) {
	window := windowStateFromRecord(state.RuleWindowRecord{History: []bool{}, TimedHistory: []rules.WindowSample{}})
	record := ruleWindowRecord(window)
	if record.History != nil || record.TimedHistory != nil {
		t.Fatalf("empty histories = %+v, want nil slices", record)
	}
}
