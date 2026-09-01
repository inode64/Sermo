package config

import (
	"slices"
	"testing"

	"sermo/internal/checks"
)

func TestAvailabilityWatchNamesHonorsSLAOverride(t *testing.T) {
	cfg := &Config{Global: Global{Raw: map[string]any{
		SectionWatches: map[string]any{
			"clock-forced": map[string]any{WatchKeyCheck: map[string]any{
				checks.CheckKeyType: checks.CheckTypeClock,
				checks.CheckKeySLA:  true,
			}},
			"clock-default": map[string]any{WatchKeyCheck: map[string]any{
				checks.CheckKeyType: checks.CheckTypeClock,
			}},
			"tcp-silenced": map[string]any{WatchKeyCheck: map[string]any{
				checks.CheckKeyType: checks.CheckTypeTCP,
				checks.CheckKeySLA:  false,
			}},
			"tcp-default": map[string]any{WatchKeyCheck: map[string]any{
				checks.CheckKeyType: checks.CheckTypeTCP,
			}},
		},
	}}}

	want := []string{"clock-forced", "tcp-default"}
	if got := cfg.AvailabilityWatchNames(); !slices.Equal(got, want) {
		t.Fatalf("AvailabilityWatchNames() = %v, want %v", got, want)
	}
}
