package checks

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestSampleTimeouts consolidates the former per-sampler timeout-message tests:
// each sampler, given an already-expired context, must return an error mentioning
// the timeout duration rather than hanging or returning a partial result.
func TestSampleTimeouts(t *testing.T) {
	cases := []struct {
		name string
		fn   func(ctx context.Context, dur time.Duration) error
	}{
		{"TallyEntries", func(ctx context.Context, dur time.Duration) error {
			_, err := TallyEntries(ctx, t.TempDir(), "any", false, false, dur)
			return err
		}},
		{"SamplePathSize", func(ctx context.Context, dur time.Duration) error {
			_, err := SamplePathSize(ctx, t.TempDir(), false, dur)
			return err
		}},
		{"SampleHdparm", func(ctx context.Context, dur time.Duration) error {
			_, err := SampleHdparm(ctx, slowHdparmRunner{}, "/dev/sda", true, false, dur)
			return err
		}},
		{"SampleSmart", func(ctx context.Context, dur time.Duration) error {
			_, err := SampleSmart(ctx, slowSmartRunner{}, "/dev/sda", dur)
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
			defer cancel()
			<-ctx.Done()

			err := c.fn(ctx, time.Millisecond)
			if err == nil {
				t.Fatal("expected timeout error")
			}
			if !strings.Contains(err.Error(), "timeout after 1ms") {
				t.Fatalf("error = %q, want timeout after duration", err.Error())
			}
		})
	}
}
