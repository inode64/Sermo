package checks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultZombieSampler
	}
	count, ok := sampler()
	if !ok {
		return c.result(false, "zombies: cannot read /proc", start)
	}
	met := compareFloat(float64(count), c.op, c.value)
	res := c.result(met, fmt.Sprintf("%d zombie processes", count), start)
	res.Data = map[string]any{DataKeyZombies: count, fieldValue: count}
	return res
}

// SampleZombies returns one live count of zombie processes using the default
// /proc scanner. ok is false when /proc cannot be read.
func SampleZombies() (count uint64, ok bool) { return defaultZombieSampler() }

// defaultZombieSampler counts processes whose /proc/<pid>/stat run state is "Z".
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
		if procRunState(pid) == "Z" {
			n++
		}
	}
	return n, true
}

// procRunState returns the run-state field of /proc/<pid>/stat (R, S, D, Z, ...),
// or "" if it cannot be read. The comm field may contain spaces and parentheses,
// so the state is the first token after the final ')'.
func procRunState(pid int) string {
	data, err := os.ReadFile(filepath.Join(procRootPath, strconv.Itoa(pid), "stat"))
	if err != nil {
		return ""
	}
	s := string(data)
	paren := strings.LastIndex(s, ")")
	if paren < 0 {
		return ""
	}
	fields := strings.Fields(s[paren+1:])
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
