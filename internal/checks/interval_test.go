package checks

import (
	"strings"
	"testing"
	"time"
)

func TestResolveInterval(t *testing.T) {
	for _, tt := range []struct {
		name       string
		interval   time.Duration
		resolution time.Duration
		cycles     int
		warning    string
	}{
		{"absent", 0, 30 * time.Second, 1, ""},
		{"negative interval", -time.Second, time.Second, 1, ""},
		{"zero resolution", time.Minute, 0, 1, ""},
		{"negative resolution", time.Minute, -time.Second, 1, ""},
		{"below half cycle", 10 * time.Second, 30 * time.Second, 1, "below the 30s resolution; running every cycle"},
		{"half cycle", 15 * time.Second, 30 * time.Second, 1, "not a multiple of the 30s resolution; running every 30s"},
		{"rounded up", 45 * time.Second, 30 * time.Second, 2, "running every 1m0s"},
		{"rounded down", 44 * time.Second, 30 * time.Second, 1, "running every 30s"},
		{"exact", time.Minute, 30 * time.Second, 2, ""},
		{"large exact", 1000 * time.Hour, 30 * time.Second, 120000, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cycles, warning := ResolveInterval(tt.interval, tt.resolution)
			if cycles != tt.cycles || (tt.warning == "" && warning != "") || !strings.Contains(warning, tt.warning) {
				t.Fatalf("ResolveInterval = (%d, %q), want (%d, containing %q)", cycles, warning, tt.cycles, tt.warning)
			}
		})
	}
}
