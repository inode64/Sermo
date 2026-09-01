package app

import (
	"testing"
	"time"
)

func TestClockOrNow(t *testing.T) {
	fixed := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		clock func() time.Time
		want  time.Time
	}{
		{name: "injected", clock: func() time.Time { return fixed }, want: fixed},
		{name: "wall clock"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := clockOrNow(tc.clock)()
			if tc.clock == nil {
				if got.IsZero() {
					t.Fatal("clockOrNow(nil) returned the zero time")
				}
				return
			}
			if !got.Equal(tc.want) {
				t.Fatalf("clockOrNow(clock)() = %v, want %v", got, tc.want)
			}
		})
	}
}
