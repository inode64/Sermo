package app

import (
	"testing"

	"sermo/internal/web"
)

// TestDedupeWatchReadingsCollapsesRepeatedContext pins that a multi-metric watch
// prints its interface identity once, not once per metric.
func TestDedupeWatchReadingsCollapsesRepeatedContext(t *testing.T) {
	in := []web.WatchReading{
		{Field: "interface", Label: "Interface", Value: "eth0"},
		{Field: "mac", Label: "MAC", Value: "34:5a:60:00:1c:92"},
		{Field: "state", Label: "State", Value: "up"},
		{Field: "interface", Label: "Interface", Value: "eth0"},
		{Field: "mac", Label: "MAC", Value: "34:5a:60:00:1c:92"},
		{Field: "speed", Label: "Speed", Value: "25000 Mbps"},
	}
	got := dedupeWatchReadings(in)
	var fields []string
	for _, r := range got {
		fields = append(fields, r.Field)
	}
	want := []string{"interface", "mac", "state", "speed"}
	if len(fields) != len(want) {
		t.Fatalf("fields = %v, want %v", fields, want)
	}
	for i := range want {
		if fields[i] != want[i] {
			t.Fatalf("fields = %v, want %v", fields, want)
		}
	}
}

// TestDedupeWatchReadingsKeepsEveryFailure guards the dangerous half: two
// metrics failing at once share a field name, and collapsing them would report
// only one of the two findings.
func TestDedupeWatchReadingsKeepsEveryFailure(t *testing.T) {
	in := []web.WatchReading{
		{Field: watchReadingFieldError, Label: "Error", Error: "eth0 state down"},
		{Field: "interface", Label: "Interface", Value: "eth0"},
		{Field: watchReadingFieldError, Label: "Error", Error: "eth0 errors +91"},
		{Field: "interface", Label: "Interface", Value: "eth0"},
		{Field: watchReadingFieldWarning, Label: "Warning", Warning: "eth0 speed 25000->1000"},
	}
	got := dedupeWatchReadings(in)
	var problems []string
	for _, r := range got {
		if r.Error != "" {
			problems = append(problems, r.Error)
		}
		if r.Warning != "" {
			problems = append(problems, r.Warning)
		}
	}
	if len(problems) != 3 {
		t.Fatalf("kept %d problem rows (%v), want all three", len(problems), problems)
	}
}
