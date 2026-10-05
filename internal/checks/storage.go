package checks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"syscall"
	"time"

	"sermo/internal/severity"
)

// StorageStats is one filesystem's usage, computed from statfs. Beyond block space
// it carries inode accounting, so a watch can catch "disk full" by inode
// exhaustion (many tiny files) even when bytes are free. InodesTotal == 0 means
// the filesystem does not report inodes (e.g. btrfs); inode predicates then never
// fire instead of misreading 0/0.
type StorageStats struct {
	UsedPct    float64
	FreePct    float64
	UsedBytes  uint64
	FreeBytes  uint64
	TotalBytes uint64

	InodesUsedPct float64
	InodesFreePct float64
	InodesFree    uint64
	InodesTotal   uint64
}

// StorageUsageFunc reports usage for the filesystem containing path. Injected for
// tests; the default uses statfs.
type StorageUsageFunc func(path string) (StorageStats, error)

// storageCheck verifies a filesystem at path: optionally that it is mounted as
// expected, and that its space/inode predicates hold. OK=true
// means an alert condition: a mount problem OR a crossed threshold. Folding mount
// in here means a filesystem's mount and space are configured once, and a space
// check is never fooled by an unmounted path reading the parent filesystem.
type storageCheck struct {
	base
	path         string
	preds        []levelPred
	usage        StorageUsageFunc
	mount        mountCond
	mountSampler MountSamplerFunc
}

func (c storageCheck) Run(_ context.Context) Result {
	start := time.Now()
	data := map[string]any{DataKeyPath: c.path}

	sampler := samplerOr(c.mountSampler, DefaultMounts)
	mounts, mountErr := sampler()

	// Mount verification takes precedence: a wrong/absent mount makes the space
	// numbers meaningless (statfs would report the parent filesystem).
	switch {
	case c.mount.active:
		if mountErr != nil {
			data[DataKeyMountSampleError] = mountErr.Error()
			res := c.unavailableResult("mount "+c.path+": "+mountErr.Error(), start)
			res.Data = data
			return res
		}
		reason, info := c.mount.evaluate(mounts, c.path)
		storageMountData(data, info != nil, info)
		if reason != "" {
			if c.mount.expectMount {
				data[DataKeyMountFailure] = MountFailureMissing
			}
			return c.mountFailure(c.path+" "+reason, data, start)
		}
		if len(c.preds) == 0 {
			if err := c.mountAnswers(); err != nil {
				return c.hungMount(err, data, start)
			}
			res := c.result(false, c.path+" mounted as expected", start)
			res.Data = data
			return res
		}
	case mountErr == nil:
		// The mount metadata is presentation data for the daemon-published
		// snapshot. A usage-only check remains independent of a failed mount-table
		// read, so a transient display detail never changes its alert outcome.
		info := MountForPath(mounts, c.path)
		storageMountData(data, info != nil, info)
	default:
		data[DataKeyMountSampleError] = mountErr.Error()
	}

	usage := c.usage
	if usage == nil {
		usage = statfsUsage
	}
	st, err := boundedUsage(usage, c.path, c.timeout)
	if err != nil {
		if c.assertsMounted() && errors.Is(err, errStatfsHung) {
			return c.hungMount(err, data, start)
		}
		data[DataKeySampleError] = err.Error()
		res := c.unavailableResult(fmt.Sprintf("statfs %s: %v", c.path, err), start)
		if errors.Is(err, errStatfsHung) && len(c.preds) > 0 {
			// Like a missing mount, a hung one is an outage for whatever uses
			// it, not an advisory of the space ladder.
			res.Severity = severity.Max(res.Severity.Resolved(), severity.Error)
		}
		res.Data = data
		return res
	}
	values := map[string]float64{
		fieldUsedPct:   st.UsedPct,
		fieldFreePct:   st.FreePct,
		fieldUsedBytes: float64(st.UsedBytes),
		fieldFreeBytes: float64(st.FreeBytes),
	}
	// Inode fields are only comparable when the filesystem reports inodes; on a
	// 0-inode filesystem an inode predicate is "unknown" and so cannot hold (the
	// level check is an AND), which keeps it from misfiring.
	if st.InodesTotal > 0 {
		values[fieldInodesUsedPct] = st.InodesUsedPct
		values[fieldInodesFreePct] = st.InodesFreePct
		values[fieldInodesFree] = float64(st.InodesFree)
	}
	ok := levelPredsHold(c.preds, values)
	res := c.grade(c.result(ok, fmt.Sprintf("%s used %.1f%% free %.1f%% inodes %.1f%% used", c.path, st.UsedPct, st.FreePct, st.InodesUsedPct), start), values)
	data[DataKeyUsedPct] = st.UsedPct
	data[DataKeyFreePct] = st.FreePct
	data[DataKeyUsedBytes] = st.UsedBytes
	data[DataKeyFreeBytes] = st.FreeBytes
	data[DataKeyTotalBytes] = st.TotalBytes
	data[DataKeyInodesUsedPct] = st.InodesUsedPct
	data[DataKeyInodesFreePct] = st.InodesFreePct
	data[DataKeyInodesFree] = st.InodesFree
	data[DataKeyInodesTotal] = st.InodesTotal
	data[DataKeyValue] = firstPredValue(c.preds, values, st.UsedPct)
	res.Data = data
	return res
}

// Mount failures a storage check with `mounted: true` reports under
// DataKeyMountFailure: the path is not mounted, or it is and does not answer.
const (
	MountFailureMissing = "missing"
	MountFailureHung    = "hung"
)

// mountFailure is the failing result of a mount assertion. A declared severity
// grades the space thresholds (the first rung of a used/free ladder): a wrong,
// absent or hung mount is never merely advisory. A mount-only check keeps its
// declaration.
func (c storageCheck) mountFailure(message string, data map[string]any, start time.Time) Result {
	res := c.result(true, message, start)
	if len(c.preds) > 0 {
		res = raiseSeverity(res, severity.Error)
	}
	res.Data = data
	return res
}

// assertsMounted reports whether the check requires path to be a mount. Such
// a path is asserted to answer as well: a hung mount fails that assertion like
// a missing one, so the watch window grades it and then.remount can repair it,
// rather than it being a probe that could not observe.
func (c storageCheck) assertsMounted() bool {
	return c.mount.active && c.mount.expectMount
}

// mountAnswers probes a mount-only check's path; only a hung answer counts, as
// any other statfs error says nothing a mount-only check asserts.
func (c storageCheck) mountAnswers() error {
	if !c.assertsMounted() {
		return nil
	}
	if err := probeStatfs(c.usage, c.path, c.timeout); errors.Is(err, errStatfsHung) {
		return err
	}
	return nil
}

func (c storageCheck) hungMount(err error, data map[string]any, start time.Time) Result {
	data[DataKeyMountFailure] = MountFailureHung
	data[DataKeySampleError] = err.Error()
	return c.mountFailure(fmt.Sprintf("%s %v", c.path, err), data, start)
}

func storageMountData(data map[string]any, mounted bool, info *Mount) {
	data[DataKeyMounted] = mounted
	if info == nil {
		return
	}
	data[DataKeyFSType], data[DataKeyDevice] = info.FSType, info.Device
	data[DataKeyMountPoint] = info.MountPoint
	data[DataKeyOptions] = strings.Join(info.Options, ",")
}

// statfsUsage is the default StorageUsageFunc backed by statfs(2).
// statfsInFlight holds the paths whose statfs has not returned yet. On a hard
// network mount whose server is gone, statfs blocks in the kernel and cannot be
// interrupted; the check reports it unavailable after its timeout, and later
// cycles fail fast instead of stacking another blocked call on the same mount.
var statfsInFlight sync.Map

// errStatfsHung marks a statfs that did not answer: the mount is hung.
var errStatfsHung = errors.New("hung mount")

// probeStatfs asks whether path answers statfs within timeout, sharing the
// in-flight guard of the usage sample.
func probeStatfs(usage StorageUsageFunc, path string, timeout time.Duration) error {
	if usage == nil {
		usage = statfsUsage
	}
	_, err := boundedUsage(usage, path, timeout)
	return err
}

// StatfsAnswers reports whether path answers statfs within timeout: nil when it
// does, an error wrapping the hung-mount cause when it does not.
func StatfsAnswers(path string, timeout time.Duration) error {
	return probeStatfs(nil, path, timeout)
}

// boundedUsage runs usage for path within timeout (unbounded when timeout is
// not positive).
func boundedUsage(usage StorageUsageFunc, path string, timeout time.Duration) (StorageStats, error) {
	if timeout <= 0 {
		return usage(path)
	}
	if _, busy := statfsInFlight.LoadOrStore(path, struct{}{}); busy {
		return StorageStats{}, fmt.Errorf("%w: an earlier statfs is still blocked", errStatfsHung)
	}
	type answer struct {
		stats StorageStats
		err   error
	}
	done := make(chan answer, 1)
	go func() {
		defer statfsInFlight.Delete(path)
		stats, err := usage(path)
		done <- answer{stats, err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case got := <-done:
		return got.stats, got.err
	case <-timer.C:
		return StorageStats{}, fmt.Errorf("%w: no answer within %s", errStatfsHung, timeout)
	}
}

func statfsUsage(path string) (StorageStats, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return StorageStats{}, fmt.Errorf("statfs %s: %w", path, err)
	}
	return storageStatsFromStatfs(path, &s)
}

// storageStatsFromStatfs converts statfs(2) counters. The percentages use df's
// base, used/(used+available): blocks reserved for root are neither, so on a
// filesystem with a 5 % reserve a total-based used_pct tops out near 95 % while
// every unprivileged write already fails with ENOSPC. used_pct and free_pct
// therefore sum to 100 and match df's Use%; TotalBytes stays the raw size.
func storageStatsFromStatfs(path string, s *syscall.Statfs_t) (StorageStats, error) {
	if s.Bsize <= 0 {
		return StorageStats{}, fmt.Errorf("statfs %q returned invalid block size %d", path, s.Bsize)
	}
	bsize := uint64(s.Bsize)
	total := s.Blocks * bsize
	free := s.Bavail * bsize // space available to unprivileged users
	used := total - s.Bfree*bsize
	var usedPct, freePct float64
	if base := used + free; base > 0 {
		usedPct = float64(used) / float64(base) * percentScale
		freePct = float64(free) / float64(base) * percentScale
	}

	// Inode accounting (f_files/f_ffree); 0 total means the filesystem does not
	// track inodes. Files/Ffree are already uint64 on every Linux GOARCH we target.
	inodesTotal := s.Files
	inodesFree := s.Ffree
	var inUsedPct, inFreePct float64
	if inodesTotal > 0 {
		inUsedPct = float64(inodesTotal-inodesFree) / float64(inodesTotal) * percentScale
		inFreePct = float64(inodesFree) / float64(inodesTotal) * percentScale
	}

	return StorageStats{
		UsedPct: usedPct, FreePct: freePct,
		UsedBytes: used, FreeBytes: free, TotalBytes: total,
		InodesUsedPct: inUsedPct, InodesFreePct: inFreePct,
		InodesFree: inodesFree, InodesTotal: inodesTotal,
	}, nil
}
