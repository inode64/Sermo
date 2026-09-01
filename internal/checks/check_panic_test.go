package checks

import (
	"context"
	"strings"
	"testing"
)

type panicCheck struct{ base }

func (panicCheck) Run(context.Context) Result { panic("boom") }

type panicMetadataCheck struct{}

func (panicMetadataCheck) Name() string               { return "metadata-fallback" }
func (panicMetadataCheck) Run(context.Context) Result { panic("run") }
func (panicMetadataCheck) resultMetadata() Result     { panic("metadata") }

type panicNameCheck struct{}

func (panicNameCheck) Name() string               { panic("name") }
func (panicNameCheck) Run(context.Context) Result { panic("run") }

// A panic in one check must fail only that check, never crash the process,
// and the surrounding checks must still return their results in order.
func TestRunRecoversPerCheckPanic(t *testing.T) {
	built := []Built{
		{Check: binaryCheck{base: base{name: "ok-before"}, path: "/bin/sh"}},
		{Check: panicCheck{base: base{name: "boom"}}, Optional: true},
		{Check: binaryCheck{base: base{name: "ok-after"}, path: "/bin/sh"}},
	}
	results := Run(context.Background(), built, 0)
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3", len(results))
	}
	if results[0].Check != "ok-before" || !results[0].OK {
		t.Fatalf("first check = %+v, want ok-before OK", results[0])
	}
	if results[1].Check != "boom" || results[1].OK || !results[1].Optional || !results[1].Unavailable || !strings.Contains(results[1].Message, "panicked") {
		t.Fatalf("panicking check = %+v, want a failed optional result naming the panic", results[1])
	}
	if results[2].Check != "ok-after" || !results[2].OK {
		t.Fatalf("third check = %+v, want ok-after OK", results[2])
	}
}

func TestExecutePreservesPanickingCheckMetadata(t *testing.T) {
	check := withSummary(panicCheck{
		name:      "boom",
		service:   "database",
		condition: true,
		reports:   ReportsCondition}, map[string]any{CheckKeySummary: "custom ${value}"})

	result := Execute(context.Background(), check)
	if result.Check != "boom" || result.Service != "database" {
		t.Fatalf("result identity = %+v, want database/boom", result)
	}
	if !result.Condition || result.Reports != ReportsCondition {
		t.Fatalf("result reporting metadata = %+v, want condition mode", result)
	}
	if result.OK || !result.Unavailable || !strings.Contains(result.Message, "boom") {
		t.Fatalf("result = %+v, want unavailable panic observation", result)
	}
}

func TestExecuteRecoversWhenMetadataPanics(t *testing.T) {
	result := Execute(context.Background(), panicMetadataCheck{})
	if result.Check != "metadata-fallback" || !result.Unavailable || !strings.Contains(result.Message, "run") {
		t.Fatalf("result = %+v, want unavailable run panic with fallback name", result)
	}
}

func TestExecuteRecoversWhenNamePanics(t *testing.T) {
	result := Execute(context.Background(), panicNameCheck{})
	if result.Check != "unknown" || !result.Unavailable || !strings.Contains(result.Message, "run") {
		t.Fatalf("result = %+v, want unavailable run panic with unknown name", result)
	}
}
