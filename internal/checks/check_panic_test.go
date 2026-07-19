package checks

import (
	"context"
	"strings"
	"testing"
)

type panicCheck struct{ base }

func (panicCheck) Run(context.Context) Result { panic("boom") }

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
	if results[1].Check != "boom" || results[1].OK || !results[1].Optional || !strings.Contains(results[1].Message, "panicked") {
		t.Fatalf("panicking check = %+v, want a failed optional result naming the panic", results[1])
	}
	if results[2].Check != "ok-after" || !results[2].OK {
		t.Fatalf("third check = %+v, want ok-after OK", results[2])
	}
}
