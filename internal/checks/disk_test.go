package checks

import (
	"context"
	"testing"
)

func fakeDisk(usedPct, freePct float64, freeBytes, totalBytes uint64) func(string) (DiskStats, error) {
	return func(string) (DiskStats, error) {
		return DiskStats{UsedPct: usedPct, FreePct: freePct, FreeBytes: freeBytes, TotalBytes: totalBytes}, nil
	}
}

func TestDiskCheckUsedPctBreached(t *testing.T) {
	c := diskCheck{
		base:  base{name: "disk", service: ""},
		path:  "/",
		preds: []diskPred{{field: "used_pct", op: ">=", value: 90}},
		usage: fakeDisk(92, 8, 100, 1000),
	}
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("expected OK (threshold crossed), got %+v", res)
	}
	if res.Data["used_pct"] != 92.0 || res.Data["path"] != "/" {
		t.Fatalf("unexpected data: %+v", res.Data)
	}
}

func TestDiskCheckUsedPctNotBreached(t *testing.T) {
	c := diskCheck{
		base:  base{name: "disk"},
		path:  "/",
		preds: []diskPred{{field: "used_pct", op: ">=", value: 90}},
		usage: fakeDisk(50, 50, 500, 1000),
	}
	if c.Run(context.Background()).OK {
		t.Fatal("expected not OK below threshold")
	}
}

func TestDiskCheckMultiPredAnd(t *testing.T) {
	// used_pct >= 90 AND free_pct < 5 -> only both true fires.
	c := diskCheck{
		base:  base{name: "disk"},
		path:  "/",
		preds: []diskPred{{"used_pct", ">=", 90}, {"free_pct", "<", 5}},
		usage: fakeDisk(92, 8, 80, 1000), // used crossed, free not (8 !< 5)
	}
	if c.Run(context.Background()).OK {
		t.Fatal("expected not OK when one predicate fails (AND)")
	}
}

func TestDiskCheckStatError(t *testing.T) {
	c := diskCheck{
		base:  base{name: "disk"},
		path:  "/nope",
		preds: []diskPred{{"used_pct", ">=", 90}},
		usage: func(string) (DiskStats, error) { return DiskStats{}, context.DeadlineExceeded },
	}
	if c.Run(context.Background()).OK {
		t.Fatal("expected not OK on stat error")
	}
}
