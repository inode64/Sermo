package checks

import (
	"strings"
	"testing"

	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
)

// A command check with a unit publishes its first numeric token as the value
// series, quotes only the first stdout line in the message, and records no
// sample (never a failure) when the output has no leading number.
func TestCommandCheckUnitPublishesNumericValue(t *testing.T) {
	c := commandCheck{
		name:       "queue",
		runner:     execxtest.Fixed(execx.Result{ExitCode: 0, Stdout: "17\nnoise line two\n"}, nil),
		argv:       []string{"/bin/exim", "-bpc"},
		expectExit: []int{0},
		numeric:    true,
	}
	res := c.Run(t.Context())
	if !res.OK {
		t.Fatalf("check failed: %s", res.Message)
	}
	if res.Data[DataKeyValue] != 17.0 {
		t.Fatalf("value = %v, want 17", res.Data[DataKeyValue])
	}
	if strings.Contains(res.Message, "noise") || !strings.HasPrefix(res.Message, "17: ") {
		t.Fatalf("message must quote only the first line: %q", res.Message)
	}

	c.runner = execxtest.Fixed(execx.Result{ExitCode: 0, Stdout: "not a number"}, nil)
	res = c.Run(t.Context())
	if !res.OK {
		t.Fatalf("non-numeric output must not fail the check: %s", res.Message)
	}
	if _, present := res.Data[DataKeyValue]; present {
		t.Fatal("non-numeric output must publish no value sample")
	}
}
