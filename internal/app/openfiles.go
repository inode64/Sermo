package app

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"sermo/internal/checks"
	"sermo/internal/process"
)

// openFilesTallyTTL bounds how often the host-wide open-files-per-mount scan runs
// from the dashboard. The scan walks every process's /proc/<pid>/fd, so it is the
// most expensive host probe; caching it for this window means all storage watches
// on one dashboard poll share a single scan, and repeated polls re-scan at most
// once per TTL (the smart/hdparm interval-bound pattern, see heavyLiveViewTypes).
const openFilesTallyTTL = time.Minute

// openFilesByMountCached returns open-file counts keyed by mount point, computed
// at most once per openFilesTallyTTL and shared across every storage watch. The
// lock is held across the scan so concurrent dashboard requests coalesce onto a
// single walk instead of each launching their own.
func (b *WebBackend) openFilesByMountCached(mounts []checks.Mount) map[string]int64 {
	now := time.Now
	if b.now != nil {
		now = b.now
	}
	at := now()

	b.openFilesMu.Lock()
	defer b.openFilesMu.Unlock()
	if b.openFilesTally != nil && at.Sub(b.openFilesTallyAt) < openFilesTallyTTL {
		return b.openFilesTally
	}
	scan := b.openFilesSampler
	if scan == nil {
		scan = scanOpenFilesByMount
	}
	b.openFilesTally = scan(mounts)
	b.openFilesTallyAt = at
	return b.openFilesTally
}

// scanOpenFilesByMount walks every process's open file descriptors and tallies,
// per mount point, the fds whose target resolves to an absolute path under that
// mount (using the same longest-prefix resolver as the space reading,
// checks.MountForPath). Sockets, pipes and anonymous fds — whose /proc/<pid>/fd
// symlink target is not an absolute path — are skipped, so the count reflects
// open files on the filesystem. It reads only local procfs (readlink returns the
// kernel's stored path string; it never stats or resolves the target), so there
// is no remote-filesystem blocking risk. A still-open but deleted file is counted
// against the mount its former path was under.
func scanOpenFilesByMount(mounts []checks.Mount) map[string]int64 {
	tally := map[string]int64{}
	pids, err := process.OSReader{}.PIDs()
	if err != nil {
		return tally
	}
	for _, pid := range pids {
		fdDir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
		entries, err := os.ReadDir(fdDir)
		if err != nil {
			continue // process exited or its fd directory is not readable
		}
		for _, entry := range entries {
			target, err := os.Readlink(filepath.Join(fdDir, entry.Name()))
			if err != nil || !filepath.IsAbs(target) {
				continue
			}
			target = strings.TrimSuffix(target, " (deleted)")
			if m := checks.MountForPath(mounts, filepath.Clean(target)); m != nil {
				tally[m.MountPoint]++
			}
		}
	}
	return tally
}
