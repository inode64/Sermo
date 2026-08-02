package state

// This file owns the operator-facing spelling of the retention cutoff consumed
// by PruneEvents and CompactHistory. Both the `sermoctl --before` flag and the
// web API's `?before=` query accept the same two forms, so they parse through
// one definition here rather than one parser per surface.

import (
	"errors"
	"fmt"
	"time"
)

// ParseCutoff reads a retention cutoff written either as a non-future RFC3339
// timestamp or as a positive duration relative to now ("1h" means "before
// now-1h"). An empty value is not an error: it yields the zero time, which
// PruneEvents and CompactHistory read as "no cutoff".
//
// label names the input in error messages so each surface reports its own
// spelling ("--before" for the CLI flag, "before" for the query parameter).
func ParseCutoff(label, value string, now time.Time) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	// The two forms are mutually exclusive — no RFC3339 timestamp parses as a
	// duration, and no duration parses as a timestamp — so the order in which
	// they are tried does not change which form wins.
	if d, err := time.ParseDuration(value); err == nil {
		if d <= 0 {
			return time.Time{}, fmt.Errorf("invalid %s: duration must be positive", label)
		}
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		if t.After(now) {
			return time.Time{}, fmt.Errorf("invalid %s: timestamp must not be in the future", label)
		}
		return t, nil
	}
	return time.Time{}, errors.New("invalid " + label +
		": use a non-future RFC3339 timestamp (e.g. 2026-06-13T12:00:00Z) or positive duration (e.g. 1h, 30m)")
}
