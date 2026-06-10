package checks

import (
	"context"
	"fmt"
	"sermo/internal/cfgval"
	"strconv"
	"strings"
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

// diskCheck verifies a filesystem at path: optionally that it is mounted as
// expected (mount conditions), and that its space/inode predicates hold. OK=true
// means an alert condition: a mount problem OR a crossed threshold. Folding mount
// in here means a filesystem's mount and space are configured once, and a space
// check is never fooled by an unmounted path reading the parent filesystem.
type diskCheck struct {
	base
	path         string
	preds        []diskPred
	usage        DiskUsageFunc
	mount        mountCond
	mountSampler MountSamplerFunc
}

func (c diskCheck) Run(_ context.Context) Result {
	start := time.Now()
	data := map[string]any{"path": c.path}

	// Mount verification takes precedence: a wrong/absent mount makes the space
	// numbers meaningless (statfs would report the parent filesystem).
	if c.mount.active {
		sampler := c.mountSampler
		if sampler == nil {
			sampler = defaultMountSampler
		}
		mounts, err := sampler()
		if err != nil {
			return c.result(false, "mount "+c.path+": "+err.Error(), start)
		}
		mounted, problem, reason, info := c.mount.evaluate(mounts, c.path)
		data["mounted"] = mounted
		if info != nil {
			data["fstype"], data["device"] = info.FSType, info.Device
			data["options"] = strings.Join(info.Options, ",")
		}
		if problem {
			res := c.result(true, c.path+" "+reason, start)
			res.Data = data
			return res
		}
		if len(c.preds) == 0 {
			res := c.result(false, c.path+" mounted as expected", start)
			res.Data = data
			return res
		}
	}

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
	data["used_pct"] = st.UsedPct
	data["free_pct"] = st.FreePct
	data["free_bytes"] = st.FreeBytes
	data["total_bytes"] = st.TotalBytes
	data["inodes_used_pct"] = st.InodesUsedPct
	data["inodes_free_pct"] = st.InodesFreePct
	data["inodes_free"] = st.InodesFree
	data["inodes_total"] = st.InodesTotal
	// value is the first predicate's reading, so a hook sees the breaching number.
	data["value"] = st.UsedPct
	if len(c.preds) > 0 {
		if v, ok := values[c.preds[0].field]; ok {
			data["value"] = v
		}
	}
	res.Data = data
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

// DefaultDiskUsage reports disk usage using the host statfs implementation.
func DefaultDiskUsage(path string) (DiskStats, error) {
	return statfsUsage(path)
}

// parseDiskPreds reads the space/inode predicates from a disk entry (each
// {op, value}). The set may be empty — a disk check is valid with only mount
// conditions — so the "at least one of predicate/mount" requirement is enforced
// by the builder and config validation, not here.
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
		op := cfgval.AsString(m["op"])
		if !validDiskOp(op) {
			return nil, fmt.Errorf("%s has invalid op %q", field, op)
		}
		val, err := strconv.ParseFloat(cfgval.String(m["value"]), 64)
		if err != nil {
			return nil, fmt.Errorf("%s value %q is not numeric", field, cfgval.String(m["value"]))
		}
		preds = append(preds, diskPred{field: field, op: op, value: val})
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
