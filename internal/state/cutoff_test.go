package state

import (
	"testing"
	"time"
)

func TestParseCutoffDurationIsPast(t *testing.T) {
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	got, err := ParseCutoff("before", "1h", now)
	if err != nil {
		t.Fatalf("ParseCutoff(1h): %v", err)
	}
	if want := now.Add(-time.Hour); !got.Equal(want) {
		t.Fatalf("ParseCutoff(1h) = %v, want %v", got, want)
	}
	if got, err := ParseCutoff("before", "", now); err != nil || !got.IsZero() {
		t.Fatalf("ParseCutoff(empty) = %v, %v, want zero time and no error", got, err)
	}
	stamp := now.Add(-2 * time.Hour)
	if got, err := ParseCutoff("before", stamp.Format(time.RFC3339), now); err != nil || !got.Equal(stamp) {
		t.Fatalf("ParseCutoff(RFC3339) = %v, %v, want %v", got, err, stamp)
	}
}

func TestParseCutoffRejectsUnsafeCutoffs(t *testing.T) {
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	for _, input := range []string{"0", "-1h", now.Add(time.Hour).Format(time.RFC3339), "yesterday"} {
		got, err := ParseCutoff("--before", input, now)
		if err == nil {
			t.Fatalf("ParseCutoff(%q) = %v, want error", input, got)
		}
		if got := err.Error(); len(got) == 0 || got[:len("invalid --before")] != "invalid --before" {
			t.Fatalf("ParseCutoff(%q) error = %q, want the label named", input, got)
		}
	}
}
