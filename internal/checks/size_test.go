package checks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSizer drives sizeCheck with scripted sizes and a controllable clock.
type fakeSizer struct {
	sizes []int64
	i     int
	now   time.Time
}

func (f *fakeSizer) sample(context.Context, string) (int64, error) {
	s := f.sizes[f.i]
	if f.i < len(f.sizes)-1 {
		f.i++
	}
	return s, nil
}

func (f *fakeSizer) clock() time.Time { return f.now }

func newSizeCheck(grow int64, window time.Duration, fz *fakeSizer) *sizeCheck {
	return &sizeCheck{
		base:    base{name: "s", timeout: time.Second},
		path:    "/x",
		growBy:  grow,
		window:  window,
		sampler: fz.sample,
		clock:   fz.clock,
		state:   &sizeState{},
	}
}

const gib = 1 << 30

func TestSizeGrowthAlerts(t *testing.T) {
	fz := &fakeSizer{sizes: []int64{1 * gib, 1 * gib, 3 * gib}, now: time.Unix(0, 0)}
	c := newSizeCheck(1*gib, time.Hour, fz)

	// First cycle: baseline only, no growth -> ok (no alert).
	if r := c.Run(context.Background()); r.OK {
		t.Fatalf("first cycle must not alert: %s", r.Message)
	}
	// 20 min later, unchanged size -> no alert.
	fz.now = fz.now.Add(20 * time.Minute)
	if r := c.Run(context.Background()); r.OK {
		t.Fatalf("steady size must not alert: %s", r.Message)
	}
	// 20 min later, grew 1GiB -> 3GiB (=2GiB over window) -> alert.
	fz.now = fz.now.Add(20 * time.Minute)
	r := c.Run(context.Background())
	if !r.OK {
		t.Fatalf("a 2GiB growth over 40m must alert: %s", r.Message)
	}
	if r.Data["growth_bytes"].(int64) != 2*gib {
		t.Fatalf("growth_bytes = %v, want %d", r.Data["growth_bytes"], 2*gib)
	}
}

func TestSizeDecreaseDoesNotAlert(t *testing.T) {
	fz := &fakeSizer{sizes: []int64{5 * gib, 1 * gib}, now: time.Unix(0, 0)}
	c := newSizeCheck(1*gib, time.Hour, fz)
	_ = c.Run(context.Background()) // baseline 5GiB
	fz.now = fz.now.Add(10 * time.Minute)
	if r := c.Run(context.Background()); r.OK {
		t.Fatalf("a shrinking file must never alert: %s", r.Message)
	}
}

func TestSizeWindowPrunesOldGrowth(t *testing.T) {
	// Grows slowly: +0.5GiB every 40min. Over any 1h window the growth is <1GiB,
	// so it must never alert even though the total over 2h is >1GiB.
	fz := &fakeSizer{sizes: []int64{1 * gib, 1*gib + gib/2, 2 * gib, 2*gib + gib/2}, now: time.Unix(0, 0)}
	c := newSizeCheck(1*gib, time.Hour, fz)
	for step := range 4 {
		if r := c.Run(context.Background()); r.OK {
			t.Fatalf("step %d: slow growth within window must not alert: %s", step, r.Message)
		}
		fz.now = fz.now.Add(40 * time.Minute)
	}
}

func TestBuildAndRunSizeCheckRealFile(t *testing.T) {
	// End-to-end via Build with the default sampler against a real file.
	path := filepath.Join(t.TempDir(), "f.bin")
	if err := os.WriteFile(path, make([]byte, 1024), 0o600); err != nil {
		t.Fatal(err)
	}
	built, warns := Build(map[string]any{
		"grow": map[string]any{"type": "size", "path": path, "grow_by": "1GB", "within": "1h"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("size check should build: warns=%v", warns)
	}
	sc, ok := built[0].Check.(*sizeCheck)
	if !ok || sc.growBy != 1<<30 || sc.window != time.Hour {
		t.Fatalf("built = %T %+v", built[0].Check, built[0].Check)
	}
	// First run baselines a small file: no alert, and it reads the real size.
	r := sc.Run(context.Background())
	if r.OK {
		t.Fatalf("baseline run must not alert: %s", r.Message)
	}
	if r.Data["current_bytes"].(int64) != 1024 {
		t.Fatalf("current_bytes = %v, want 1024", r.Data["current_bytes"])
	}
}

func TestSizeCheckHonorsCanceledContext(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.bin"), make([]byte, 1024), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &sizeCheck{
		base:   base{name: "s", timeout: time.Second},
		path:   root,
		growBy: 1,
		window: time.Hour,
		state:  &sizeState{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := c.Run(ctx)
	if res.OK {
		t.Fatal("canceled size check should fail")
	}
	if !strings.Contains(res.Message, "cancelled") {
		t.Fatalf("message = %q, want cancelled", res.Message)
	}
}

func TestBuildSizeCheckErrors(t *testing.T) {
	cases := []map[string]any{
		{"type": "size", "grow_by": "1GB", "within": "1h"},                    // no path
		{"type": "size", "path": "/x", "within": "1h"},                        // no grow_by
		{"type": "size", "path": "/x", "grow_by": "nonsense", "within": "1h"}, // bad grow_by
		{"type": "size", "path": "/x", "grow_by": "1GB"},                      // no within
		{"type": "size", "path": "/x", "grow_by": "1GB", "within": "nope"},    // bad within
	}
	for i, entry := range cases {
		_, warns := Build(map[string]any{"s": entry}, Deps{DefaultTimeout: time.Second})
		if len(warns) == 0 {
			t.Fatalf("case %d should warn: %v", i, entry)
		}
	}
}

func TestHumanizeSigned(t *testing.T) {
	// Negative deltas get a sign and the magnitude of |n|; zero and positive
	// deltas render without a sign.
	if got := humanizeSigned(-2048); got != "-2.0 kB" {
		t.Errorf("humanizeSigned(-2048) = %q, want -2.0 kB", got)
	}
	if got := humanizeSigned(2048); got != "2.0 kB" {
		t.Errorf("humanizeSigned(2048) = %q, want 2.0 kB", got)
	}
	if got := humanizeSigned(0); got != "0 B" {
		t.Errorf("humanizeSigned(0) = %q, want 0 B", got)
	}
}

func TestSizeGrowthAtExactThreshold(t *testing.T) {
	// Growth of exactly grow_by trips the check (growth >= growBy, not >).
	fz := &fakeSizer{sizes: []int64{1 * gib, 2 * gib}, now: time.Unix(0, 0)}
	c := newSizeCheck(1*gib, time.Hour, fz)
	if r := c.Run(context.Background()); r.OK {
		t.Fatalf("baseline cycle must not alert: %s", r.Message)
	}
	fz.now = fz.now.Add(20 * time.Minute)
	if r := c.Run(context.Background()); !r.OK {
		t.Fatalf("growth of exactly 1GiB == limit must alert: %s", r.Message)
	}
}
