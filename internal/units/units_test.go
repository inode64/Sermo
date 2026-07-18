package units

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// duration_cases.json is the shared JS↔Go parity table: tests/web/format.spec.js
// runs the same cases against fmtDuration in internal/web/src/format.js.
func TestHumanizeDurationParityTable(t *testing.T) {
	raw, err := os.ReadFile("testdata/duration_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Seconds int64  `json:"seconds"`
		Want    string `json:"want"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("empty parity table")
	}
	for _, c := range cases {
		if got := HumanizeDuration(time.Duration(c.Seconds) * time.Second); got != c.Want {
			t.Errorf("HumanizeDuration(%ds) = %q, want %q", c.Seconds, got, c.Want)
		}
	}
}

func TestHumanizeDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{-5 * time.Second, "0s"},           // negative clamps to "0s"
		{1500 * time.Millisecond, "1.5s"},  // sub-second falls back to stdlib
		{time.Hour + time.Second, "1h 1s"}, // zero components are skipped
	}
	for _, c := range cases {
		if got := HumanizeDuration(c.in); got != c.want {
			t.Errorf("HumanizeDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
