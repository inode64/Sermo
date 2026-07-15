package checks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"sermo/internal/process"
)

const (
	procPIDStatFile       = "stat"
	procStatRunStateIndex = 0
)

// ZombieSamplerFunc counts the zombie (defunct) processes, reporting ok = false
// when /proc cannot be read. Injected for tests; the default scans /proc.
type ZombieSamplerFunc func() (uint64, bool)

// zombieCheck is a level check for zombie process count.
type zombieCheck struct {
	base
	op      string
	value   float64
	sampler ZombieSamplerFunc
}

func (c zombieCheck) Run(_ context.Context) Result {
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultZombieSampler
	}
	return runThresholdCheck(c.base, c.op, c.value, sampler, "zombies: cannot read /proc",
		func(count uint64) string { return fmt.Sprintf("%d zombie processes", count) }, DataKeyZombies)
}

// SampleZombies returns one live count of zombie processes using the default
// /proc scanner. ok is false when /proc cannot be read.
func SampleZombies() (count uint64, ok bool) { return defaultZombieSampler() }

// defaultZombieSampler counts processes whose /proc/<pid>/stat run state is zombie.
func defaultZombieSampler() (uint64, bool) {
	entries, err := os.ReadDir(procRootPath)
	if err != nil {
		return 0, false
	}
	var n uint64
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		if procRunState(pid) == process.ProcStateZombie {
			n++
		}
	}
	return n, true
}

// procRunState returns the run-state field of /proc/<pid>/stat (R, S, D, Z, ...),
// or "" if it cannot be read. The comm field may contain spaces and parentheses,
// so the state is the first token after the final ')'.
func procRunState(pid int) string {
	data, err := os.ReadFile(filepath.Join(procRootPath, strconv.Itoa(pid), procPIDStatFile))
	if err != nil {
		return ""
	}
	s := string(data)
	paren := strings.LastIndex(s, ")")
	if paren < 0 {
		return ""
	}
	fields := strings.Fields(s[paren+1:])
	if len(fields) <= procStatRunStateIndex {
		return ""
	}
	return fields[procStatRunStateIndex]
}
