package metrics

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestCachedSampleFreshnessAndFailures(t *testing.T) {
	var cache cachedSample[int]
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	calls := 0
	complete := true
	var readErr error
	read := func() (int, bool, error) { calls++; return calls, complete, readErr }
	check := func(at time.Time, age time.Duration, want int, wantErr error) {
		t.Helper()
		var got int
		err := cache.readInto(at, age, read, &got)
		if got != want || !errors.Is(err, wantErr) {
			t.Fatalf("sample = %d, %v; want %d, %v", got, err, want, wantErr)
		}
	}
	check(now, 2*time.Second, 1, nil)
	check(now.Add(time.Second), 2*time.Second, 1, nil)
	// A caller with a shorter bound refreshes the shared sample.
	check(now.Add(time.Second), time.Second, 2, nil)
	check(now.Add(2*time.Second), 2*time.Second, 2, nil)
	check(now.Add(2*time.Second), 0, 3, nil)
	readErr = errors.New("procfs unavailable")
	check(now.Add(4*time.Second), time.Second, 4, readErr)
	readErr = nil
	// Even a permissive caller cannot recover the sample invalidated by failure.
	check(now.Add(4*time.Second), time.Hour, 5, nil)
	complete = false
	check(now.Add(6*time.Second), time.Second, 6, nil)
	complete = true
	check(now.Add(6*time.Second), time.Hour, 7, nil)
	// A backwards clock never makes a future sample fresh indefinitely.
	check(now, time.Hour, 8, nil)
}

func TestCachedSampleCoalescesConcurrentReaders(t *testing.T) {
	var cache cachedSample[int]
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	calls := 0
	read := func() (int, bool, error) { calls++; return 42, true, nil }
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			var got int
			err := cache.readInto(now, time.Second, read, &got)
			if err != nil || got != 42 {
				t.Errorf("sample = %d, %v", got, err)
			}
		})
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("host reads = %d, want 1", calls)
	}
}
