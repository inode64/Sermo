package process

import (
	"context"
	"testing"
)

func TestReapCancellationStopsSignals(t *testing.T) {
	for _, phase := range []string{"entry", "rediscovery", "user lookup"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sig := &recSignaler{}
			procs := []Process{killableProc(100)}
			r := newReaper(sig, nil)
			r.Rediscover = func() []Process {
				if phase == "rediscovery" {
					cancel()
				}
				return procs
			}
			if phase == "entry" {
				cancel()
			}
			if phase == "user lookup" {
				r.ResolveUser = func(string) (uint32, bool) { cancel(); return 110, true }
			}
			result := r.Reap(ctx, procs, killPolicy)
			want := 0
			if phase == "rediscovery" {
				want = 1
			}
			if len(sig.calls) != want || len(result.Remaining) != 1 {
				t.Fatalf("signals = %v; result = %+v", sig.calls, result)
			}
		})
	}
}

func TestSignalCancellationDuringUserLookup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := &recSignaler{}
	r := newReaper(sig, nil)
	r.ResolveUser = func(string) (uint32, bool) { cancel(); return 110, true }
	result := r.Signal(ctx, []Process{killableProc(100)}, killPolicy.KillOnlyIf, signalNames["HUP"])
	if len(sig.calls) != 0 || len(result.Failed) != 1 {
		t.Fatalf("signals = %v; result = %+v", sig.calls, result)
	}
}
