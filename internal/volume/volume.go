// Package volume grows an LVM-backed filesystem natively in Go, shelling out
// only to the LVM and filesystem tools that have no native Go API (lvs, vgs,
// lvextend, resize2fs, xfs_growfs, btrfs). The orchestration — resolving a path
// to its mount and logical volume, checking the volume group's free space,
// capping the request and sequencing extend-then-grow — is all Go.
//
// Scope: LVM logical volumes with an ext2/3/4, xfs or btrfs filesystem. Non-LVM
// devices and other layouts (DRBD, plain partitions) are rejected rather than
// half-handled.
package volume

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"sermo/internal/checks"
	"sermo/internal/execx"
)

// DefaultCommandTimeout bounds each LVM/filesystem command the expander runs.
const DefaultCommandTimeout = 30 * time.Second

const (
	cmdBtrfs     = "btrfs"
	cmdLVExtend  = "lvextend"
	cmdLVS       = "lvs"
	cmdResize2FS = "resize2fs"
	cmdVGS       = "vgs"
	cmdXFSGrowFS = "xfs_growfs"

	btrfsSubcommandFilesystem = "filesystem"
	btrfsSubcommandResize     = "resize"
	btrfsResizeMax            = "max"

	lvmFlagNoHeadings     = "--noheadings"
	lvmFlagOutput         = "-o"
	lvmFlagSeparator      = "--separator"
	lvmFlagUnits          = "--units"
	lvmFlagNoSuffix       = "--nosuffix"
	lvmLVFields           = "vg_name,lv_name"
	lvmVGFreeField        = "vg_free"
	lvmCSVSeparator       = ","
	lvmByteUnit           = "b"
	devPathPrefix         = "/dev/"
	lvmLVPathFormat       = devPathPrefix + "%s/%s"
	lvExtendSizeArgFormat = "-L+%db"
)

// Mount is one entry of the mount table.
type Mount struct {
	Device     string
	Mountpoint string
	FSType     string
}

// MountSource returns the current mount table. Injected for tests; the default
// reads /proc/mounts.
type MountSource func() ([]Mount, error)

// Target is a resolved expansion target: a path, the filesystem mounted at its
// containing mount point, and the LVM logical volume backing it.
type Target struct {
	Path       string
	Mountpoint string
	FSType     string
	Device     string
	VG         string
	LV         string
}

// Result describes a completed expansion.
type Result struct {
	VG        string
	LV        string
	FSType    string
	GrewBytes int64
}

// Expander runs LVM/filesystem expansion through an injected command runner.
type Expander struct {
	Runner  execx.Runner
	Mounts  MountSource   // nil -> /proc/mounts
	Timeout time.Duration // per command; 0 -> DefaultCommandTimeout
}

func (e Expander) timeout() time.Duration {
	if e.Timeout > 0 {
		return e.Timeout
	}
	return DefaultCommandTimeout
}

func commandFailure(prefix string, err error, res execx.Result, timeout time.Duration) error {
	msg := execx.OperatorFailure(err, res, timeout)
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("%s: %s", prefix, msg)
}

func (e Expander) mountTable() ([]Mount, error) {
	if e.Mounts != nil {
		return e.Mounts()
	}
	return procMounts()
}

// Resolve maps path to the filesystem at its containing mount point and the LVM
// logical volume backing that mount. The mount lookup is native (/proc/mounts);
// the device→VG/LV mapping uses `lvs` (LVM has no Go API). A device that is not
// an LVM logical volume is an error.
func (e Expander) Resolve(ctx context.Context, path string) (Target, error) {
	mounts, err := e.mountTable()
	if err != nil {
		return Target{}, fmt.Errorf("read mounts: %w", err)
	}
	m, ok := containingMount(mounts, path)
	if !ok {
		return Target{}, fmt.Errorf("no mounted filesystem contains %q", path)
	}
	t := Target{Path: path, Mountpoint: m.Mountpoint, FSType: m.FSType, Device: m.Device}

	to := e.timeout()
	res, err := execx.Run(ctx, e.Runner, to, cmdLVS, lvmFlagNoHeadings, lvmFlagOutput, lvmLVFields, lvmFlagSeparator, lvmCSVSeparator, m.Device)
	if err != nil {
		return Target{}, commandFailure(fmt.Sprintf("%q is not an LVM volume (%s %s)", path, cmdLVS, m.Device), err, res, to)
	}
	vg, lv, ok := strings.Cut(strings.TrimSpace(res.Stdout), ",")
	if !ok || vg == "" || lv == "" {
		return Target{}, fmt.Errorf("%q is not an LVM volume", path)
	}
	t.VG, t.LV = strings.TrimSpace(vg), strings.TrimSpace(lv)
	return t, nil
}

// ExpandPath resolves path to its LVM-backed filesystem and grows it by up to
// by bytes (see Resolve and Expand). It is the one-call entry point the watch
// action uses.
func (e Expander) ExpandPath(ctx context.Context, path string, by int64) (Result, error) {
	t, err := e.Resolve(ctx, path)
	if err != nil {
		return Result{}, err
	}
	return e.Expand(ctx, t, by)
}

// Expand grows the logical volume backing t by up to by bytes, then grows the
// filesystem to fill it. The request is capped to the volume group's free space
// (like a manual operator would); a full volume group is an error. Only
// ext2/3/4, xfs and btrfs filesystems are grown.
func (e Expander) Expand(ctx context.Context, t Target, by int64) (Result, error) {
	if !growableFS(t.FSType) {
		return Result{}, fmt.Errorf("unsupported filesystem %q for %s", t.FSType, t.Mountpoint)
	}
	free, err := e.vgFreeBytes(ctx, t.VG)
	if err != nil {
		return Result{}, err
	}
	if free <= 0 {
		return Result{}, fmt.Errorf("no free space in volume group %q to expand %s", t.VG, t.Mountpoint)
	}
	grow := by
	if grow > free {
		grow = free // use all that is available, as the operator script does
	}
	if grow <= 0 {
		// Config validation already requires a positive expand.by, but Expand is
		// exported: guard so a zero/negative request never reaches lvextend as a
		// no-op or malformed `-L+<n>b` argument.
		return Result{}, fmt.Errorf("expand size must be positive, got %d bytes for %s", by, t.Mountpoint)
	}

	lv := fmt.Sprintf(lvmLVPathFormat, t.VG, t.LV)
	to := e.timeout()
	res, err := execx.Run(ctx, e.Runner, to, cmdLVExtend, fmt.Sprintf(lvExtendSizeArgFormat, grow), lv)
	if err != nil {
		return Result{}, commandFailure(cmdLVExtend+" "+lv, err, res, to)
	}
	if err := e.growFS(ctx, t); err != nil {
		return Result{}, err
	}
	return Result{VG: t.VG, LV: t.LV, FSType: t.FSType, GrewBytes: grow}, nil
}

// Filesystem types Expand can grow.
const (
	fsExt2  = "ext2"
	fsExt3  = "ext3"
	fsExt4  = "ext4"
	fsXFS   = "xfs"
	fsBtrfs = "btrfs"
)

// growableFS reports whether Expand can grow a filesystem of this type.
func growableFS(fstype string) bool {
	switch fstype {
	case fsExt2, fsExt3, fsExt4, fsXFS, fsBtrfs:
		return true
	default:
		return false
	}
}

// growFS grows the filesystem onto the newly extended volume. ext* resize by
// device; xfs and btrfs resize by mount point.
func (e Expander) growFS(ctx context.Context, t Target) error {
	lv := fmt.Sprintf(lvmLVPathFormat, t.VG, t.LV)
	to := e.timeout()
	var (
		res execx.Result
		err error
	)
	switch t.FSType {
	case fsExt2, fsExt3, fsExt4:
		res, err = execx.Run(ctx, e.Runner, to, cmdResize2FS, lv)
	case fsXFS:
		res, err = execx.Run(ctx, e.Runner, to, cmdXFSGrowFS, t.Mountpoint)
	case fsBtrfs:
		res, err = execx.Run(ctx, e.Runner, to, cmdBtrfs, btrfsSubcommandFilesystem, btrfsSubcommandResize, btrfsResizeMax, t.Mountpoint)
	}
	if err != nil {
		return commandFailure(fmt.Sprintf("grow %s filesystem on %s", t.FSType, t.Mountpoint), err, res, to)
	}
	return nil
}

// vgFreeBytes reports the free space of volume group vg in bytes.
func (e Expander) vgFreeBytes(ctx context.Context, vg string) (int64, error) {
	to := e.timeout()
	res, err := execx.Run(ctx, e.Runner, to, cmdVGS, lvmFlagNoHeadings, lvmFlagOutput, lvmVGFreeField, lvmFlagUnits, lvmByteUnit, lvmFlagNoSuffix, vg)
	if err != nil {
		return 0, commandFailure(cmdVGS+" "+vg, err, res, to)
	}
	free, err := parseInt(res.Stdout)
	if err != nil {
		return 0, fmt.Errorf("parse vg_free for %q: %w", vg, err)
	}
	return free, nil
}

// containingMount returns the mount whose mount point is the longest prefix of
// path (an exact match, a parent directory, or "/" as the fallback).
func containingMount(mounts []Mount, path string) (Mount, bool) {
	path = cleanMountpoint(path)
	var best Mount
	bestLen := -1 // every mount point normalizes to at least "/" (len 1), so -1 means "none yet"
	for _, m := range mounts {
		mp := cleanMountpoint(m.Mountpoint)
		if mp == path || mp == "/" || strings.HasPrefix(path, mp+"/") {
			if len(mp) > bestLen {
				best, bestLen = m, len(mp)
			}
		}
	}
	return best, bestLen >= 0
}

// List returns real storage mounts, skipping pseudo filesystems (tmpfs, proc,
// sysfs, cgroup, ...), autofs placeholders and duplicate mount points. It is
// the candidate list the volume wizard offers. mounts is injectable for tests;
// nil reads /proc/mounts.
func List(mounts MountSource) ([]Mount, error) {
	if mounts == nil {
		mounts = procMounts
	}
	all, err := mounts()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Mount
	for _, m := range all {
		if !IsStorageMount(m) || seen[m.Mountpoint] {
			continue
		}
		seen[m.Mountpoint] = true
		out = append(out, m)
	}
	return pruneNestedSameDeviceMounts(out), nil
}

// IsStorageMount reports whether a mount table entry is a real storage
// filesystem candidate for generated storage watches.
func IsStorageMount(m Mount) bool {
	if m.Mountpoint == "" || m.FSType == "" {
		return false
	}
	if pseudoFilesystem(m.FSType) {
		return false
	}
	if strings.HasPrefix(m.Device, devPathPrefix) {
		return true
	}
	return storageFilesystem(m.FSType)
}

func pseudoFilesystem(fstype string) bool {
	switch fstype {
	case "autofs", "binfmt_misc", "bpf", "cgroup", "cgroup2", "configfs",
		"debugfs", "devpts", "devtmpfs", "efivarfs", "fusectl", "hugetlbfs",
		"mqueue", "nsfs", "proc", "pstore", "ramfs", "rpc_pipefs",
		"securityfs", "sysfs", "tracefs", "tmpfs":
		return true
	default:
		return false
	}
}

func storageFilesystem(fstype string) bool {
	switch fstype {
	case "ceph", "cifs", "glusterfs", "gfs2", "lustre", "nfs", "nfs4",
		"ocfs2", "smb3", "zfs":
		return true
	default:
		return strings.HasPrefix(fstype, "fuse.")
	}
}

func pruneNestedSameDeviceMounts(mounts []Mount) []Mount {
	sort.SliceStable(mounts, func(i, j int) bool {
		return len(cleanMountpoint(mounts[i].Mountpoint)) < len(cleanMountpoint(mounts[j].Mountpoint))
	})
	out := make([]Mount, 0, len(mounts))
	for _, m := range mounts {
		if hasParentMountOnSameDevice(out, m) {
			continue
		}
		out = append(out, m)
	}
	return out
}

func hasParentMountOnSameDevice(existing []Mount, child Mount) bool {
	for _, parent := range existing {
		if parent.Device == child.Device && nestedMountpoint(parent.Mountpoint, child.Mountpoint) {
			return true
		}
	}
	return false
}

func nestedMountpoint(parent, child string) bool {
	parent = cleanMountpoint(parent)
	child = cleanMountpoint(child)
	if parent == child {
		return false
	}
	if parent == "/" {
		return strings.HasPrefix(child, "/")
	}
	return strings.HasPrefix(child, parent+"/")
}

func cleanMountpoint(path string) string {
	path = strings.TrimRight(path, "/")
	if path == "" {
		return "/"
	}
	return path
}

// procMounts reads the mount table via the shared /proc/mounts parser
// (internal/checks owns the escaping rules), mapped to this package's shape.
func procMounts() ([]Mount, error) {
	entries, err := checks.DefaultMounts()
	if err != nil {
		return nil, err
	}
	out := make([]Mount, 0, len(entries))
	for _, m := range entries {
		out = append(out, Mount{Device: m.Device, Mountpoint: m.MountPoint, FSType: m.FSType})
	}
	return out, nil
}

// parseInt trims and parses a decimal integer from command output.
func parseInt(s string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
}
