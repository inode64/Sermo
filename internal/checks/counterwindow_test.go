package checks

import (
	"testing"
	"time"
)

func TestCounterWindowAdvance(t *testing.T) {
	t0 := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	type step struct {
		after      time.Duration
		count      int
		wantGrowth int
		wantSpan   time.Duration
	}
	cases := []struct {
		name   string
		window time.Duration
		steps  []step
	}{
		{
			// The default 30s engine tick lands a few ms past a 30s window: the
			// previous sample must stay the baseline, or growth is +0 forever.
			name:   "window no longer than the sampling interval",
			window: 30 * time.Second,
			steps: []step{
				{0, 10, 0, 0},
				{30*time.Second + 5*time.Millisecond, 15, 5, 30*time.Second + 5*time.Millisecond},
				{60*time.Second + 9*time.Millisecond, 22, 7, 30*time.Second + 4*time.Millisecond},
			},
		},
		{
			name:   "window longer than the interval baselines on the oldest sample inside it",
			window: 30 * time.Second,
			steps: []step{
				{0, 1, 0, 0},
				{10 * time.Second, 2, 1, 10 * time.Second},
				{20 * time.Second, 3, 2, 20 * time.Second},
				{40 * time.Second, 9, 7, 30 * time.Second},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &counterWindow{}
			for i, s := range tc.steps {
				growth, span := w.advance(t0.Add(s.after), s.count, tc.window)
				if growth != s.wantGrowth || span != s.wantSpan {
					t.Fatalf("step %d: growth, span = %d, %v; want %d, %v", i, growth, span, s.wantGrowth, s.wantSpan)
				}
			}
		})
	}
}
