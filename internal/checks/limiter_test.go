package checks

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sermo/internal/servicemgr"
)

func TestLimiterSharedAcrossBatchesAndInlineChecks(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	limiter := NewLimiter(2)
	var running, peak atomic.Int32
	started := make(chan struct{}, 12)
	release := make(chan struct{})
	deps := Deps{Limiter: limiter, DefaultTimeout: time.Second, Status: func(ctx context.Context) (servicemgr.Status, error) {
		n := running.Add(1)
		defer running.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		started <- struct{}{}
		select {
		case <-release:
			return servicemgr.StatusActive, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}
	entry := map[string]any{CheckKeyType: CheckTypeService, "expect": string(servicemgr.StatusActive), CheckKeySummary: "observed"}
	var wg sync.WaitGroup
	for i := range 4 {
		built, issues := Build(map[string]any{fmt.Sprintf("service-%d", i): entry}, deps)
		if len(issues) != 0 {
			t.Fatal(issues)
		}
		wg.Go(func() {
			if res := Run(ctx, built, 0)[0]; !res.OK {
				t.Errorf("batch: %+v", res)
			}
		})
	}
	inline, err := BuildInline("watch", entry, deps)
	if err != nil {
		t.Fatal(err)
	}
	wg.Go(func() {
		if res := Execute(ctx, inline); !res.OK {
			t.Errorf("inline: %+v", res)
		}
	})
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("independent probes did not overlap")
		}
	}
	close(release)
	wg.Wait()
	if got := peak.Load(); got != 2 {
		t.Fatalf("peak concurrency = %d, want 2", got)
	}
}

func TestLimiterQueueCancellationAndResize(t *testing.T) {
	limiter := NewLimiter(2)
	for range 2 {
		if err := limiter.acquire(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	limiter.SetLimit(1)
	limiter.release()
	check := withLimiter(fakeCheck{res: Result{Check: "probe", OK: true}}, limiter, time.Millisecond)
	res := Execute(t.Context(), check)
	if !res.Unavailable || res.OK {
		t.Fatalf("queued timeout = %+v", res)
	}
	limiter.release()
	if res := Execute(t.Context(), check); !res.OK {
		t.Fatalf("slot leaked: %+v", res)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if res := Execute(ctx, check); !res.Unavailable {
		t.Fatalf("canceled probe started: %+v", res)
	}
	limiter.SetLimit(2)
	for range 2 {
		if err := limiter.acquire(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	limiter.release()
	limiter.release()
}

func TestLimiterReleasesAfterPanic(t *testing.T) {
	limiter := NewLimiter(1)
	if res := Execute(t.Context(), withLimiter(panicCheck{}, limiter, time.Second)); !res.Unavailable {
		t.Fatalf("panic = %+v", res)
	}
	if res := Execute(t.Context(), withLimiter(fakeCheck{res: Result{OK: true}}, limiter, time.Millisecond)); !res.OK {
		t.Fatalf("panic leaked slot: %+v", res)
	}
}
