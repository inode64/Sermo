package process

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Discoverer finds a service's processes through its selectors and the process
// tree (section 21).
type Discoverer struct {
	Reader      Reader
	ResolveUser UserResolver
}

// NewDiscoverer returns a Discoverer backed by the host /proc and passwd db.
func NewDiscoverer() Discoverer {
	return Discoverer{Reader: OSReader{}, ResolveUser: OSUserResolver}
}

// Discover applies pidfile then command_match selectors, then adds descendants
// from the process tree, deduplicated by PID (section 21). Non-fatal problems
// (missing pidfile, dead pid) are returned as warnings.
func (d Discoverer) Discover(selectors []Selector) ([]Process, []string) {
	reader := d.Reader
	if reader == nil {
		reader = OSReader{}
	}
	resolve := d.ResolveUser
	if resolve == nil {
		resolve = OSUserResolver
	}

	var warnings []string
	snapshot := snapshotIdentities(reader)

	found := map[int]Process{}
	var order []int
	add := func(id Identity, role, source string) {
		if _, ok := found[id.PID]; ok {
			return
		}
		found[id.PID] = toProcess(id, role, source)
		order = append(order, id.PID)
	}

	// 1. pidfiles.
	for _, sel := range selectors {
		if sel.Type != SelectorPidfile {
			continue
		}
		pid, err := readPidfile(sel.Path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("pidfile %q (%s): %v", sel.Path, sel.Name, err))
			continue
		}
		id, ok := snapshot[pid]
		if !ok {
			warnings = append(warnings, fmt.Sprintf("pidfile %q (%s) references pid %d which is not running", sel.Path, sel.Name, pid))
			continue
		}
		add(id, sel.Name, sourcePidfile)
	}

	// 2. command_match across the snapshot.
	for _, pid := range sortedPIDs(snapshot) {
		id := snapshot[pid]
		for _, sel := range selectors {
			if sel.Type == SelectorCommandMatch && d.matches(sel, id, resolve) {
				add(id, sel.Name, sourceCommand)
				break
			}
		}
	}

	// 3. descendants from the process tree.
	for _, pid := range descendants(snapshot, order) {
		add(snapshot[pid], "child", sourceChild)
	}

	result := make([]Process, 0, len(order))
	for _, pid := range order {
		result = append(result, found[pid])
	}
	return result, warnings
}

// Process states reported by ObserveState (section 12).
const (
	StateRunning = "running"
	StateZombie  = "zombie"
	StateAbsent  = "absent"
)

// ObserveState reports the state of processes matching an exe/user selector
// (section 12), using the exact resolved-exe and real-UID rules of section 21:
//
//   - running: at least one live (non-zombie) process matches;
//   - zombie:  matches exist but all are defunct;
//   - absent:  no process matches.
func (d Discoverer) ObserveState(exe, user string) string {
	reader := d.Reader
	if reader == nil {
		reader = OSReader{}
	}
	resolve := d.ResolveUser
	if resolve == nil {
		resolve = OSUserResolver
	}
	sel := Selector{Type: SelectorCommandMatch, Exe: exe, User: user}

	matched, live := false, false
	for _, id := range snapshotIdentities(reader) {
		if !d.matches(sel, id, resolve) {
			continue
		}
		matched = true
		if id.State != "Z" {
			live = true
		}
	}
	switch {
	case live:
		return StateRunning
	case matched:
		return StateZombie
	default:
		return StateAbsent
	}
}

// matches reports whether a process satisfies every declared field of a
// command_match selector (exe AND user when both are present, section 21).
func (d Discoverer) matches(sel Selector, id Identity, resolve UserResolver) bool {
	if sel.Exe == "" && sel.User == "" {
		return false
	}
	if sel.Exe != "" {
		if !id.ExeOK || canonicalizePath(sel.Exe) != id.Exe {
			return false
		}
	}
	if sel.User != "" {
		uid, ok := resolve(sel.User)
		if !ok || uid != id.UID {
			return false
		}
	}
	return true
}

func toProcess(id Identity, role, source string) Process {
	return Process{
		PID:     id.PID,
		PPID:    id.PPID,
		User:    id.User,
		UID:     id.UID,
		Exe:     id.Exe,
		ExeOK:   id.ExeOK,
		Cmdline: id.Cmdline,
		Role:    role,
		Source:  source,
	}
}

// descendants returns every PID reachable as a child of the seed PIDs, excluding
// the seeds themselves, in a stable order.
func descendants(snapshot map[int]Identity, seeds []int) []int {
	children := map[int][]int{}
	for pid, id := range snapshot {
		children[id.PPID] = append(children[id.PPID], pid)
	}
	for _, kids := range children {
		sort.Ints(kids)
	}

	seen := map[int]bool{}
	for _, pid := range seeds {
		seen[pid] = true
	}
	var out []int
	queue := append([]int{}, seeds...)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, child := range children[pid] {
			if seen[child] {
				continue
			}
			seen[child] = true
			out = append(out, child)
			queue = append(queue, child)
		}
	}
	return out
}

func snapshotIdentities(reader Reader) map[int]Identity {
	snapshot := map[int]Identity{}
	pids, err := reader.PIDs()
	if err != nil {
		return snapshot
	}
	for _, pid := range pids {
		if id, ok := reader.Identity(pid); ok {
			snapshot[pid] = id
		}
	}
	return snapshot
}

func sortedPIDs(snapshot map[int]Identity) []int {
	pids := make([]int, 0, len(snapshot))
	for pid := range snapshot {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids
}

// readPidfile reads a PID from a pidfile, accepting a trailing newline.
func readPidfile(path string) (int, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return 0, err
	}
	text := strings.TrimSpace(string(data))
	pid, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("invalid pid %q", text)
	}
	if pid <= 0 {
		return 0, fmt.Errorf("invalid pid %d", pid)
	}
	return pid, nil
}

// ParseSelectors extracts the `processes` section of a resolved service tree
// into typed selectors, reporting unknown or malformed entries as warnings.
func ParseSelectors(tree map[string]any) ([]Selector, []string) {
	raw, ok := tree["processes"].(map[string]any)
	if !ok {
		return nil, nil
	}

	var selectors []Selector
	var warnings []string
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		entry, ok := raw[name].(map[string]any)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("process selector %q is not a mapping", name))
			continue
		}
		sel := Selector{Name: name, Type: asString(entry["type"])}
		switch sel.Type {
		case SelectorPidfile:
			sel.Path = asString(entry["path"])
			if sel.Path == "" {
				warnings = append(warnings, fmt.Sprintf("pidfile selector %q has no path", name))
				continue
			}
		case SelectorCommandMatch:
			sel.Exe = asString(entry["exe"])
			sel.User = asString(entry["user"])
			if sel.Exe == "" && sel.User == "" {
				warnings = append(warnings, fmt.Sprintf("command_match selector %q has neither exe nor user", name))
				continue
			}
		default:
			warnings = append(warnings, fmt.Sprintf("process selector %q has unknown type %q", name, sel.Type))
			continue
		}
		selectors = append(selectors, sel)
	}
	return selectors, warnings
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}
