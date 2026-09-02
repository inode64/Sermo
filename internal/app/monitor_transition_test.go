package app

import (
	"errors"
	"testing"

	"sermo/internal/state"
)

type transitionStore struct {
	record   state.MonitorRecord
	found    bool
	readErr  error
	writeErr error
	writes   int
}

func (s *transitionStore) MonitorState(string) (state.MonitorRecord, bool, error) {
	return s.record, s.found, s.readErr
}

func (s *transitionStore) SetActive(_ string, monitored bool, source string) error {
	s.writes++
	if s.writeErr != nil {
		return s.writeErr
	}
	s.record.Active = monitored
	s.record.Source = source
	s.found = true
	return nil
}

func TestApplyMonitorTransition(t *testing.T) {
	tests := []struct {
		name      string
		found     bool
		active    bool
		monitored bool
		wantWrite bool
	}{
		{name: "missing defaults to monitored", monitored: true},
		{name: "missing can be paused", monitored: false, wantWrite: true},
		{name: "active stays active", found: true, active: true, monitored: true},
		{name: "active is paused", found: true, active: true, monitored: false, wantWrite: true},
		{name: "paused stays paused", found: true, active: false, monitored: false},
		{name: "paused is resumed", found: true, active: false, monitored: true, wantWrite: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &transitionStore{record: state.MonitorRecord{Active: tt.active, Source: "original"}, found: tt.found}
			result, err := ApplyMonitorTransition(store, "web", tt.monitored, state.SourceCLI)
			if err != nil {
				t.Fatal(err)
			}
			wantWrites := 0
			if tt.wantWrite {
				wantWrites = 1
			}
			if result.Changed != tt.wantWrite || store.writes != wantWrites {
				t.Fatalf("result/writes = %+v/%d, want changed/write %t", result, store.writes, tt.wantWrite)
			}
			if !tt.wantWrite && store.record.Source != "original" {
				t.Fatalf("no-op changed source to %q", store.record.Source)
			}
		})
	}
}

func TestApplyMonitorTransitionErrors(t *testing.T) {
	readFailure := &transitionStore{readErr: errors.New("read failed")}
	if _, err := ApplyMonitorTransition(readFailure, "web", false, state.SourceCLI); err == nil || readFailure.writes != 0 {
		t.Fatalf("read failure = %v, writes=%d", err, readFailure.writes)
	}

	writeFailure := &transitionStore{found: true, record: state.MonitorRecord{Active: true}, writeErr: errors.New("write failed")}
	if _, err := ApplyMonitorTransition(writeFailure, "web", false, state.SourceCLI); err == nil || writeFailure.writes != 1 {
		t.Fatalf("write failure = %v, writes=%d", err, writeFailure.writes)
	}
}
