package servicemgr

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	// selfCgroupPath is where the kernel publishes the calling process's cgroup.
	selfCgroupPath = "/proc/self/cgroup"
	// unifiedCgroupPrefix opens the single line cgroup v2 writes for a process
	// ("0::/system.slice/sermod.service"). A v1 hierarchy writes one line per
	// controller instead, with no such entry.
	unifiedCgroupPrefix = "0::"
)

// SelfUnitCgroupPIDs returns every PID in the caller's own control group, plus
// the name of the init unit that owns it.
//
// ok is false unless the caller runs inside a cgroup v2 systemd *service* unit.
// That restriction is the whole point: only a service unit's control group
// belongs exclusively to one unit, so only there does "everything in my cgroup
// is mine" hold. Started from a login shell the caller sits in that session's
// `.scope` alongside the operator's shell and sshd, and treating those as its
// own processes would be catastrophic — so the answer there is "no", not a guess.
//
// readFile defaults to os.ReadFile; it is injectable so callers can test without
// a real /proc and /sys.
func SelfUnitCgroupPIDs(readFile func(string) ([]byte, error)) (pids []int, unit string, ok bool) {
	if readFile == nil {
		readFile = os.ReadFile
	}
	data, err := readFile(selfCgroupPath)
	if err != nil {
		return nil, "", false
	}
	path := unifiedCgroupPath(string(data))
	// A session or machine scope ends in ".scope" and a slice in ".slice";
	// neither is a unit that owns its whole control group.
	if path == "" || !strings.HasSuffix(path, systemdServiceSuffix) {
		return nil, "", false
	}
	procs, err := readFile(filepath.Join(cgroupRoot, path, "cgroup.procs"))
	if err != nil {
		return nil, "", false
	}
	return parseCgroupProcs(procs), filepath.Base(path), true
}

// unifiedCgroupPath extracts the cgroup v2 path from /proc/self/cgroup content,
// or "" when the process is not in a unified hierarchy.
func unifiedCgroupPath(content string) string {
	for line := range strings.SplitSeq(content, serviceOutputLineSeparator) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, unifiedCgroupPrefix) {
			continue
		}
		path := strings.TrimPrefix(line, unifiedCgroupPrefix)
		if path == "" || path == "/" {
			return ""
		}
		return path
	}
	return ""
}
