package checks

import (
	"context"
	"strings"
	"testing"
)

func fakeDisk(usedPct, freePct float64, freeBytes, totalBytes uint64) func(string) (DiskStats, error) {
	return func(string) (DiskStats, error) {
		var usedBytes uint64
		if totalBytes >= freeBytes {
			usedBytes = totalBytes - freeBytes
		}
		return DiskStats{UsedPct: usedPct, FreePct: freePct, UsedBytes: usedBytes, FreeBytes: freeBytes, TotalBytes: totalBytes}, nil
	}
}

func TestStorageCheckUsedPctBreached(t *testing.T) {
	c := diskCheck{
		base:  base{name: "storage", service: ""},
		path:  "/",
		preds: []levelPred{{field: "used_pct", op: ">=", value: 90}},
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

func TestStorageCheckUsedPctNotBreached(t *testing.T) {
	c := diskCheck{
		base:  base{name: "storage"},
		path:  "/",
		preds: []levelPred{{field: "used_pct", op: ">=", value: 90}},
		usage: fakeDisk(50, 50, 500, 1000),
	}
	if c.Run(context.Background()).OK {
		t.Fatal("expected not OK below threshold")
	}
}

func TestStorageCheckMultiPredAnd(t *testing.T) {
	// used_pct >= 90 AND free_pct < 5 -> only both true fires.
	c := diskCheck{
		base:  base{name: "storage"},
		path:  "/",
		preds: []levelPred{{"used_pct", ">=", 90}, {"free_pct", "<", 5}},
		usage: fakeDisk(92, 8, 80, 1000), // used crossed, free not (8 !< 5)
	}
	if c.Run(context.Background()).OK {
		t.Fatal("expected not OK when one predicate fails (AND)")
	}
}

func TestStorageCheckFreeBytesBreached(t *testing.T) {
	c := diskCheck{
		base:  base{name: "storage"},
		path:  "/",
		preds: []levelPred{{field: "free_bytes", op: "<", value: float64(10 << 30)}},
		usage: fakeDisk(92, 8, 9<<30, 100<<30),
	}
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("expected free_bytes threshold crossed, got %+v", res)
	}
	if res.Data["value"] != float64(9<<30) || res.Data["free_bytes"] != uint64(9<<30) {
		t.Fatalf("unexpected data: %+v", res.Data)
	}
}

func TestStorageCheckUsedBytesBreached(t *testing.T) {
	c := diskCheck{
		base:  base{name: "storage"},
		path:  "/",
		preds: []levelPred{{field: "used_bytes", op: ">=", value: float64(90 << 30)}},
		usage: fakeDisk(92, 8, 8<<30, 100<<30),
	}
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("expected used_bytes threshold crossed, got %+v", res)
	}
	if res.Data["value"] != float64(92<<30) || res.Data["used_bytes"] != uint64(92<<30) {
		t.Fatalf("unexpected data: %+v", res.Data)
	}
}

func TestStorageCheckStatError(t *testing.T) {
	c := diskCheck{
		base:  base{name: "storage"},
		path:  "/nope",
		preds: []levelPred{{"used_pct", ">=", 90}},
		usage: func(string) (DiskStats, error) { return DiskStats{}, context.DeadlineExceeded },
	}
	if c.Run(context.Background()).OK {
		t.Fatal("expected not OK on stat error")
	}
}

func TestBuildStorageCheck(t *testing.T) {
	section := map[string]any{
		"d": map[string]any{
			"type":     "storage",
			"path":     "/",
			"used_pct": map[string]any{"op": ">=", "value": 90},
		},
	}
	built, warns := Build(section, Deps{DiskUsage: fakeDisk(92, 8, 80, 1000)})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(built) != 1 {
		t.Fatalf("expected 1 built check, got %d", len(built))
	}
	if !built[0].Check.Run(context.Background()).OK {
		t.Fatal("expected storage check to fire above threshold")
	}
}

func TestBuildStorageByteSizeCheck(t *testing.T) {
	section := map[string]any{
		"d": map[string]any{
			"type":       "storage",
			"path":       "/",
			"free_bytes": map[string]any{"op": "<", "value": "10G"},
		},
	}
	built, warns := Build(section, Deps{DiskUsage: fakeDisk(92, 8, 9<<30, 100<<30)})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(built) != 1 || !built[0].Check.Run(context.Background()).OK {
		t.Fatal("byte-sized storage check should build and fire below threshold")
	}
}

func TestBuildStoragePercentSuffixCheck(t *testing.T) {
	section := map[string]any{
		"d": map[string]any{
			"type":     "storage",
			"path":     "/",
			"used_pct": map[string]any{"op": ">=", "value": "90%"},
		},
	}
	built, warns := Build(section, Deps{DiskUsage: fakeDisk(92, 8, 9<<30, 100<<30)})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(built) != 1 || !built[0].Check.Run(context.Background()).OK {
		t.Fatal("percent-suffixed storage check should build and fire above threshold")
	}
}

func TestBuildStorageByteSizeCheckRejectsUnitless(t *testing.T) {
	section := map[string]any{
		"d": map[string]any{
			"type":       "storage",
			"path":       "/",
			"free_bytes": map[string]any{"op": "<", "value": 10},
		},
	}
	built, warns := Build(section, Deps{DiskUsage: fakeDisk(92, 8, 9<<30, 100<<30)})
	if len(built) != 0 {
		t.Fatalf("expected no built checks, got %d", len(built))
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "must include a size suffix") {
		t.Fatalf("expected suffix warning, got %v", warns)
	}
}

func TestBuildStorageCheckRejectsMissing(t *testing.T) {
	_, warns := Build(map[string]any{"d": map[string]any{"type": "storage"}}, Deps{})
	if len(warns) == 0 {
		t.Fatal("expected a warning for storage check without path/predicate")
	}
}

func fakeDiskStats(s DiskStats) func(string) (DiskStats, error) {
	return func(string) (DiskStats, error) { return s, nil }
}

func TestStorageCheckInodesUsedPct(t *testing.T) {
	// 9500/10000 inodes used = 95%.
	stats := DiskStats{TotalBytes: 1000, FreeBytes: 900, InodesTotal: 10000, InodesFree: 500, InodesUsedPct: 95, InodesFreePct: 5}
	breach := diskCheck{base: base{name: "d"}, path: "/", preds: []levelPred{{"inodes_used_pct", ">=", 90}}, usage: fakeDiskStats(stats)}
	if res := breach.Run(context.Background()); !res.OK {
		t.Fatalf("95%% inodes used should breach >= 90, got %q", res.Message)
	}
	if breach.Run(context.Background()).Data["value"] != 95.0 {
		t.Fatal("value should be the inodes_used_pct reading")
	}
	// Plenty of block space free, but inodes exhausted -> the inode predicate fires.
	ok := diskCheck{base: base{name: "d"}, path: "/", preds: []levelPred{{"inodes_free", "<", 1000}}, usage: fakeDiskStats(stats)}
	if !ok.Run(context.Background()).OK {
		t.Fatal("500 inodes free < 1000 should fire")
	}
}

func TestStorageCheckInodesUnavailableNeverFires(t *testing.T) {
	// A filesystem that reports no inodes (InodesTotal == 0) must not misfire an
	// inode predicate (e.g. inodes_free < N would otherwise see 0 < N and fire).
	stats := DiskStats{TotalBytes: 1000, FreeBytes: 900, InodesTotal: 0}
	c := diskCheck{base: base{name: "d"}, path: "/", preds: []levelPred{{"inodes_free", "<", 1000}}, usage: fakeDiskStats(stats)}
	if c.Run(context.Background()).OK {
		t.Fatal("inode predicate must not fire on a 0-inode filesystem")
	}
}

func TestBuildStorageInodeCheck(t *testing.T) {
	section := map[string]any{
		"d": map[string]any{
			"type":            "storage",
			"path":            "/",
			"inodes_used_pct": map[string]any{"op": ">=", "value": 90},
		},
	}
	built, warns := Build(section, Deps{DiskUsage: fakeDiskStats(DiskStats{TotalBytes: 1000, InodesTotal: 100, InodesFree: 5, InodesUsedPct: 95})})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(built) != 1 || !built[0].Check.Run(context.Background()).OK {
		t.Fatal("inode storage check should build and fire above threshold")
	}
}

func TestStorageCheckDataHasValueKey(t *testing.T) {
	// used_pct predicate -> value is used_pct.
	c := diskCheck{base: base{name: "d"}, path: "/", preds: []levelPred{{"used_pct", ">=", 90}}, usage: fakeDisk(92, 8, 80, 1000)}
	if v := c.Run(context.Background()).Data["value"]; v != 92.0 {
		t.Fatalf("value = %v, want 92.0 (used_pct)", v)
	}
	// only free_pct predicate -> value is free_pct.
	c2 := diskCheck{base: base{name: "d"}, path: "/", preds: []levelPred{{"free_pct", "<", 5}}, usage: fakeDisk(96, 4, 40, 1000)}
	if v := c2.Run(context.Background()).Data["value"]; v != 4.0 {
		t.Fatalf("value = %v, want 4.0 (free_pct)", v)
	}
}
