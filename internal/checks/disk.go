package checks

import (
	"context"
	"fmt"
	"strconv"
	"syscall"
	"time"
)

// DiskStats is one filesystem's usage, computed from statfs. Beyond block space
// it carries inode accounting, so a watch can catch "disk full" by inode
// exhaustion (many tiny files) even when bytes are free. InodesTotal == 0 means
// the filesystem does not report inodes (e.g. btrfs); inode predicates then never
// fire instead of misreading 0/0.
type DiskStats struct {
	UsedPct    float64
	FreePct    float64
	FreeBytes  uint64
	TotalBytes uint64

	InodesUsedPct float64
	InodesFreePct float64
	InodesFree    uint64
	InodesTotal   uint64
}

// DiskUsageFunc reports usage for the filesystem containing path. Injected for
// tests; the default uses statfs.
type DiskUsageFunc func(path string) (DiskStats, error)

// diskPred is one threshold predicate on a computed disk field.
type diskPred struct {
	field string // used_pct | free_pct | inodes_used_pct | inodes_free_pct | inodes_free
	op    string // >= > <= < == !=
	value float64
}

// diskCheck passes (OK=true) when every predicate is satisfied, i.e. the
// threshold is crossed (section 12, mirrors metricCheck).
type diskCheck struct {
	base
	path  string
	preds []diskPred
	usage DiskUsageFunc
}

func (c diskCheck) Run(_ context.Context) Result {
	start := time.Now()
	usage := c.usage
	if usage == nil {
		usage = statfsUsage
	}
	st, err := usage(c.path)
	if err != nil {
		return c.result(false, fmt.Sprintf("statfs %s: %v", c.path, err), start)
	}
	values := map[string]float64{"used_pct": st.UsedPct, "free_pct": st.FreePct}
	// Inode fields are only comparable when the filesystem reports inodes; on a
	// 0-inode filesystem an inode predicate is "unknown" and so cannot hold (the
	// level check is an AND), which keeps it from misfiring.
	if st.InodesTotal > 0 {
		values["inodes_used_pct"] = st.InodesUsedPct
		values["inodes_free_pct"] = st.InodesFreePct
		values["inodes_free"] = float64(st.InodesFree)
	}
	ok := true
	for _, p := range c.preds {
		v, known := values[p.field]
		if !known || !compareFloat(v, p.op, p.value) {
			ok = false
		}
	}
	res := c.result(ok, fmt.Sprintf("%s used %.1f%% free %.1f%% inodes %.1f%% used", c.path, st.UsedPct, st.FreePct, st.InodesUsedPct), start)
	res.Data = map[string]any{
		"path":            c.path,
		"used_pct":        st.UsedPct,
		"free_pct":        st.FreePct,
		"free_bytes":      st.FreeBytes,
		"total_bytes":     st.TotalBytes,
		"inodes_used_pct": st.InodesUsedPct,
		"inodes_free_pct": st.InodesFreePct,
		"inodes_free":     st.InodesFree,
		"inodes_total":    st.InodesTotal,
	}
	// value is the first predicate's reading, so a hook sees the breaching number.
	res.Data["value"] = st.UsedPct
	if len(c.preds) > 0 {
		if v, ok := values[c.preds[0].field]; ok {
			res.Data["value"] = v
		}
	}
	return res
}

func compareFloat(a float64, op string, b float64) bool {
	switch op {
	case ">=":
		return a >= b
	case ">":
		return a > b
	case "<=":
		return a <= b
	case "<":
		return a < b
	case "==":
		return a == b
	case "!=":
		return a != b
	default:
		return false
	}
}

// statfsUsage is the default DiskUsageFunc backed by statfs(2).
func statfsUsage(path string) (DiskStats, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return DiskStats{}, err
	}
	bsize := uint64(s.Bsize)
	total := s.Blocks * bsize
	free := s.Bavail * bsize // space available to unprivileged users
	used := total - s.Bfree*bsize
	var usedPct, freePct float64
	if total > 0 {
		usedPct = float64(used) / float64(total) * 100
		freePct = float64(free) / float64(total) * 100
	}

	// Inode accounting (f_files/f_ffree); 0 total means the filesystem does not
	// track inodes.
	inodesTotal := uint64(s.Files)
	inodesFree := uint64(s.Ffree)
	var inUsedPct, inFreePct float64
	if inodesTotal > 0 {
		inUsedPct = float64(inodesTotal-inodesFree) / float64(inodesTotal) * 100
		inFreePct = float64(inodesFree) / float64(inodesTotal) * 100
	}

	return DiskStats{
		UsedPct: usedPct, FreePct: freePct, FreeBytes: free, TotalBytes: total,
		InodesUsedPct: inUsedPct, InodesFreePct: inFreePct,
		InodesFree: inodesFree, InodesTotal: inodesTotal,
	}, nil
}

// parseDiskPreds reads the used_pct/free_pct predicates from a disk entry. At
// least one is required; each is {op, value}.
func parseDiskPreds(entry map[string]any) ([]diskPred, error) {
	var preds []diskPred
	for _, field := range diskPredFields {
		raw, ok := entry[field]
		if !ok {
			continue
		}
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s must be a mapping {op, value}", field)
		}
		op := asString(m["op"])
		if !validDiskOp(op) {
			return nil, fmt.Errorf("%s has invalid op %q", field, op)
		}
		val, err := strconv.ParseFloat(scalarString(m["value"]), 64)
		if err != nil {
			return nil, fmt.Errorf("%s value %q is not numeric", field, scalarString(m["value"]))
		}
		preds = append(preds, diskPred{field: field, op: op, value: val})
	}
	if len(preds) == 0 {
		return nil, fmt.Errorf("requires at least one of used_pct/free_pct/inodes_used_pct/inodes_free_pct/inodes_free")
	}
	return preds, nil
}

// diskPredFields are the predicate fields a disk check accepts: block space and
// inode accounting. Shared with config validation so both stay in step.
var diskPredFields = []string{"used_pct", "free_pct", "inodes_used_pct", "inodes_free_pct", "inodes_free"}

func validDiskOp(op string) bool {
	switch op {
	case ">=", ">", "<=", "<", "==", "!=":
		return true
	default:
		return false
	}
}
