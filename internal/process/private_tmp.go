package process

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"sermo/internal/hostfs"
	"sermo/internal/mounts"
)

const (
	procFileMountInfo = "mountinfo"
	// systemdPrivateDirMarker names the directories systemd creates for a
	// unit's PrivateTmp=yes: /tmp/systemd-private-<boot>-<unit>-<random>/tmp,
	// and the same under /var/tmp.
	systemdPrivateDirMarker = "/systemd-private-"
	// mountRootDeletedSuffix is what the kernel appends to a mount's root in
	// mountinfo once that directory is unlinked on the host.
	mountRootDeletedSuffix = "//deleted"
)

// privateTmpMountPoints are the two mounts PrivateTmp=yes gives a unit.
var privateTmpMountPoints = []string{"/tmp", "/var/tmp"}

// LostPrivateTmp is a systemd PrivateTmp mount a service's process still sees
// although its directory on the host was deleted (by hand or by a cleanup that
// does not know systemd's exclusion). The process keeps working, but systemd
// rebuilds the unit's mount namespace from that directory for every
// ExecReload, ExecStop and ExecStartPost, which then fail with
// 226/NAMESPACE until the unit restarts and gets a new one.
type LostPrivateTmp struct {
	PID        int
	MountPoint string
	// Root is the deleted systemd-private directory, as the kernel reports it.
	Root string
}

// LostPrivateTmpIn inspects one process per mount namespace among procs, other
// than Sermo's own: every process of a unit shares the namespace systemd built
// for it, and a process in the host's namespace has no PrivateTmp to lose. A
// process that exits during the scan is skipped; any other read error is
// returned, because the scan then proves nothing.
func LostPrivateTmpIn(procs []Process) ([]LostPrivateTmp, error) {
	return privateTmpScanner{readLink: hostfs.Readlink, readFile: hostfs.ReadFile}.lost(procs)
}

type privateTmpScanner struct {
	readLink func(string) (string, error)
	readFile func(string) ([]byte, error)
}

func (s privateTmpScanner) lost(procs []Process) ([]LostPrivateTmp, error) {
	own, err := s.readLink(SelfPath(ProcFileMountNamespace))
	if err != nil {
		return nil, fmt.Errorf("read own mount namespace: %w", err)
	}
	seen := map[string]bool{own: true}
	var lost []LostPrivateTmp
	for _, p := range procs {
		if p.Delegated || p.External {
			continue
		}
		namespace, err := s.readLink(PIDPath(p.PID, ProcFileMountNamespace))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read mount namespace of pid %d: %w", p.PID, err)
		}
		if seen[namespace] {
			continue
		}
		seen[namespace] = true
		data, err := s.readFile(PIDPath(p.PID, procFileMountInfo))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read mountinfo of pid %d: %w", p.PID, err)
		}
		for _, m := range mounts.ParseMountInfo(data) {
			if isLostPrivateTmp(m) {
				lost = append(lost, LostPrivateTmp{PID: p.PID, MountPoint: m.MountPoint, Root: m.Root})
			}
		}
	}
	return lost, nil
}

func isLostPrivateTmp(m mounts.MountInfo) bool {
	return slices.Contains(privateTmpMountPoints, m.MountPoint) &&
		strings.Contains(m.Root, systemdPrivateDirMarker) && strings.HasSuffix(m.Root, mountRootDeletedSuffix)
}
