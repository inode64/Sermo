package servicemgr

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"sermo/internal/hostfs"
)

// openRCBackendPIDs recognizes OpenRC's unified per-service cgroup only when a
// live init-declared pidfile names a member. Without that principal, guessing
// the first cgroup PID would attribute an orphan as the service's main process.
// Init definitions are parsed once; runtime PID and membership are read afresh.
func openRCBackendPIDs(unit string, readFile func(string) ([]byte, error)) func() []int {
	if readFile == nil {
		readFile = hostfs.ReadFile
	}
	if _, ok := openRCUnitPath(openRCInitDir, unit); !ok {
		return func() []int { return nil }
	}
	info := detectOpenRCProc(readFile, unit)
	groupPath := filepath.Join(cgroupRoot, "openrc."+unit, "cgroup.procs")
	return func() []int {
		if info.Pidfile == "" {
			return nil
		}
		pidData, err := readFile(info.Pidfile)
		if err != nil {
			return nil
		}
		principal, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
		if err != nil || principal <= 1 {
			return nil
		}
		data, err := readFile(groupPath)
		if err != nil {
			return nil
		}
		members := parseCgroupProcs(data)
		if !slices.Contains(members, principal) {
			return nil
		}
		pids := []int{principal}
		for _, pid := range members {
			if pid > 1 && !slices.Contains(pids, pid) {
				pids = append(pids, pid)
			}
		}
		return pids
	}
}
