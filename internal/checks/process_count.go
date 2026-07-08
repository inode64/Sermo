package checks

import (
	"context"
	"fmt"
	"strings"
	"time"

	"sermo/internal/process"
)

// ProcessCountFunc counts the processes matching a filter (empty fields are
// wildcards). Injected so the daemon can share its cached /proc snapshot; nil
// falls back to a self-contained scan.
type ProcessCountFunc func(user, exe, exeDir string) int

// processCountCheck compares the number of running processes against a threshold.
// Like users/zombies it is a level check: OK==true means the `count` predicate
// holds, so a watch with `count: {op: '>', value: N}` fires when there are more
// than N matching processes. With no selector it counts every process on the
// host; user/exe/exe_dir narrow it (ANDed).
type processCountCheck struct {
	base
	preds  []levelPred
	user   string
	exe    string
	exeDir string
	count  ProcessCountFunc
}

func (c processCountCheck) Run(_ context.Context) Result {
	start := time.Now()
	counter := c.count
	if counter == nil {
		counter = defaultProcessCount
	}
	n := counter(c.user, c.exe, c.exeDir)
	values := map[string]float64{DataKeyCount: float64(n)}
	ok := levelPredsHold(c.preds, values)
	res := c.result(ok, fmt.Sprintf("%d process(es)%s", n, c.scope()), start)
	res.Data = map[string]any{DataKeyCount: n, DataKeyValue: float64(n)}
	return res
}

// scope describes the active filters for the result message, e.g. " (user=www-data, exe under /usr/sbin)".
func (c processCountCheck) scope() string {
	var parts []string
	if c.user != "" {
		parts = append(parts, "user="+c.user)
	}
	if c.exe != "" {
		parts = append(parts, "exe="+c.exe)
	}
	if c.exeDir != "" {
		parts = append(parts, "exe under "+c.exeDir)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// defaultProcessCount counts via a self-contained process discovery (used when
// no shared counter is injected, e.g. host watches and the CLI).
func defaultProcessCount(user, exe, exeDir string) int {
	return process.NewDiscovererWithUserLookup(nil).CountMatching(user, exe, exeDir)
}
