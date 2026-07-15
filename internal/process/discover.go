package process

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sermo/internal/cfgval"
	"sort"
	"strconv"
	"strings"
)

// Discoverer finds a service's processes through its selectors and the process
// tree.
type Discoverer struct {
	Reader       Reader
	ResolveUser  UserResolver
	ResolveGroup UserResolver // group-name -> GID (OSGroupResolver); for command_match group
	// BackendPIDs reports backend-provided PIDs (systemd cgroup process set and
	// MainPID), tried first. Optional.
	BackendPIDs func() []int
}

// NewDiscovererWithUserLookup returns a Discoverer backed by the host /proc and
// the provided user/group lookup policy. A nil lookup uses DefaultUserLookup.
func NewDiscovererWithUserLookup(lookup *UserLookup) Discoverer {
	if lookup == nil {
		lookup = DefaultUserLookup()
	}
	return Discoverer{
		Reader:       OSReader{LookupUserName: lookup.Username},
		ResolveUser:  lookup.ResolveUser,
		ResolveGroup: lookup.ResolveGroup,
	}
}

func (d Discoverer) reader() Reader {
	if d.Reader != nil {
		return d.Reader
	}
	lookup := DefaultUserLookup()
	return OSReader{LookupUserName: lookup.Username}
}

func (d Discoverer) resolveUser() UserResolver {
	if d.ResolveUser != nil {
		return d.ResolveUser
	}
	return DefaultUserLookup().ResolveUser
}

func (d Discoverer) resolveGroup() UserResolver {
	if d.ResolveGroup != nil {
		return d.ResolveGroup
	}
	return DefaultUserLookup().ResolveGroup
}

// Discover applies backend-provided PID seeds first, then pidfile and command
// selectors, then adds descendants from the process tree, deduplicated by PID.
// Non-fatal problems (missing pidfile, dead pid) are returned as warnings.
func (d Discoverer) Discover(selectors []Selector) ([]Process, []string) {
	reader := d.reader()
	resolve := d.resolveUser()

	var warnings []string
	backendPIDs := backendPIDSeeds(d.BackendPIDs)
	if len(backendPIDs) == 0 && len(selectors) == 0 {
		return nil, nil
	}
	snapshot := snapshotIdentities(reader)

	found := map[int]Process{}
	var order []int
	var hasBackendProcess bool
	add := func(id Identity, role, source string) {
		if _, ok := found[id.PID]; ok {
			return
		}
		found[id.PID] = toProcess(id, role, source)
		order = append(order, id.PID)
	}

	// 0. backend-provided PIDs (systemd cgroup + MainPID).
	for _, pid := range backendPIDs {
		if id, ok := snapshot[pid]; ok {
			add(id, RoleMain, sourceBackend)
			hasBackendProcess = true
		}
	}

	// 1. pidfiles. Candidate paths (e.g. per-OS variants) are tried in order; the
	// first that points at a running process wins. Only when none do is the most
	// relevant failure reported.
	for i := range selectors {
		sel := &selectors[i]
		if sel.Type != SelectorPidfile {
			continue
		}
		var lastWarn string
		matched := false
		for _, path := range sel.Paths {
			pid, err := ReadPidfile(path)
			if err != nil {
				lastWarn = fmt.Sprintf("pidfile %q (%s): %v", path, sel.Name, err)
				continue
			}
			id, ok := snapshot[pid]
			if !ok {
				lastWarn = fmt.Sprintf("pidfile %q (%s) references pid %d which is not running", path, sel.Name, pid)
				continue
			}
			add(id, sel.Name, sourcePidfile)
			matched = true
			break
		}
		if !matched && lastWarn != "" && !hasBackendProcess {
			warnings = append(warnings, lastWarn)
		}
	}

	// 2. command_match across the snapshot.
	for _, pid := range sortedPIDs(snapshot) {
		id := snapshot[pid]
		for i := range selectors {
			if selectors[i].Type == SelectorCommandMatch && d.matches(&selectors[i], id, resolve) {
				add(id, selectors[i].Name, sourceCommand)
				break
			}
		}
	}

	// 3. descendants from the process tree.
	for _, pid := range descendants(snapshot, order) {
		add(snapshot[pid], RoleChild, sourceChild)
	}

	result := make([]Process, 0, len(order))
	for _, pid := range order {
		result = append(result, found[pid])
	}
	return result, warnings
}

func backendPIDSeeds(fn func() []int) []int {
	if fn == nil {
		return nil
	}
	seen := map[int]bool{}
	var seeds []int
	for _, pid := range fn() {
		if pid <= 0 || seen[pid] {
			continue
		}
		seen[pid] = true
		seeds = append(seeds, pid)
	}
	return seeds
}

// Process states reported by ObserveState.
const (
	StateRunning = "running"
	StateZombie  = "zombie"
	StateAbsent  = "absent"
	// StateSummary is the user-facing list of process watch states.
	StateSummary = StateRunning + ", " + StateZombie + ", " + StateAbsent
)

// ObserveState reports the state of processes matching an exe/user selector,
// using the exact resolved-exe and real-UID rules:
//
//   - running: at least one live (non-zombie) process matches;
//   - zombie:  matches exist but all are defunct;
//   - absent:  no process matches.
func (d Discoverer) ObserveState(exe, user string) string {
	return d.ObserveAnyState([]string{exe}, user)
}

// ObserveAnyState reports the state of processes matching any exact resolved
// executable in exes with the same real-user selector.
func (d Discoverer) ObserveAnyState(exes []string, user string) string {
	reader := d.reader()
	resolve := d.resolveUser()
	selectors := make([]Selector, 0, len(exes))
	for _, exe := range exes {
		if exe == "" {
			continue
		}
		selectors = append(selectors, Selector{Type: SelectorCommandMatch, Exe: exe, User: user})
	}
	if len(selectors) == 0 {
		return StateAbsent
	}

	matched, live := false, false
	for _, id := range snapshotIdentities(reader) {
		if !d.matchesAny(selectors, id, resolve) {
			continue
		}
		matched = true
		if id.State != ProcStateZombie {
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

// CountMatching counts processes matching the given filter. Each non-empty
// field narrows the count (ANDed); an all-empty filter counts every process on
// the host. user is the real-UID owner, exe is the exact resolved
// /proc/<pid>/exe, and exeDir matches any process whose resolved executable is
// under that directory. Reuses the Discoverer's process snapshot (shared cache
// in the daemon).
func (d Discoverer) CountMatching(user, exe, exeDir string) int {
	f, ok := d.buildProcessFilter(user, exe, exeDir)
	if !ok {
		return 0 // unknown user: nothing can match
	}
	n := 0
	for _, id := range snapshotIdentities(d.reader()) {
		if f.matchesIdentity(id) {
			n++
		}
	}
	return n
}

// CountInTree counts the service's OWN processes — its selector matches plus
// their descendants (the PID tree, parent and children) — that also pass the
// optional user/exe/exe_dir filter. An all-empty filter counts the whole tree.
// This scopes a process_count check to the service's PID set instead of the
// whole host, so it is safe against unrelated same-user/same-exe processes.
func (d Discoverer) CountInTree(selectors []Selector, user, exe, exeDir string) int {
	f, ok := d.buildProcessFilter(user, exe, exeDir)
	if !ok {
		return 0
	}
	procs, _ := d.Discover(selectors)
	n := 0
	for i := range procs {
		if f.matchesProcess(&procs[i]) {
			n++
		}
	}
	return n
}

// processFilter is the resolved user/exe/exe_dir predicate shared by the
// host-wide CountMatching and the tree-scoped CountInTree.
type processFilter struct {
	uid     uint32
	haveUID bool
	exePath string
	dir     string
}

// buildProcessFilter resolves the filter fields once; ok is false when the user
// name is unknown (nothing can match).
func (d Discoverer) buildProcessFilter(user, exe, exeDir string) (processFilter, bool) {
	var f processFilter
	if user != "" {
		u, ok := d.resolveUser()(user)
		if !ok {
			return f, false
		}
		f.uid, f.haveUID = u, true
	}
	if exe != "" {
		f.exePath = canonicalizePath(exe)
	}
	if exeDir != "" {
		f.dir = canonicalizePath(exeDir)
	}
	return f, true
}

func (f processFilter) match(uid uint32, exeOK bool, exe string) bool {
	if f.haveUID && uid != f.uid {
		return false
	}
	if f.exePath != "" && (!exeOK || exe != f.exePath) {
		return false
	}
	if f.dir != "" && (!exeOK || !pathUnder(exe, f.dir)) {
		return false
	}
	return true
}

func (f processFilter) matchesIdentity(id Identity) bool {
	return f.match(id.UID, id.ExeOK, id.Exe)
}

func (f processFilter) matchesProcess(p *Process) bool {
	return f.match(p.UID, p.ExeOK, p.Exe)
}

// pathUnder reports whether p lies under directory dir (a strict descendant), so
// "/opt/app" matches "/opt/app/bin/x" but not "/opt/application/x".
func pathUnder(p, dir string) bool {
	return strings.HasPrefix(p, strings.TrimRight(dir, string(os.PathSeparator))+string(os.PathSeparator))
}

func (d Discoverer) matchesAny(selectors []Selector, id Identity, resolve UserResolver) bool {
	for i := range selectors {
		if d.matches(&selectors[i], id, resolve) {
			return true
		}
	}
	return false
}

// StrictMatchPID reports whether pid currently matches a process selector
// that declares both exact resolved exe and real user. Pidfile-only evidence is
// intentionally ignored: callers that are about to signal a process need the
// stronger identity check used by the signaling safety invariants.
func (d Discoverer) StrictMatchPID(pid int, selectors []Selector) (Process, bool) {
	if pid <= 0 {
		return Process{}, false
	}
	id, ok := d.reader().Identity(pid)
	if !ok {
		return Process{}, false
	}
	resolve := d.resolveUser()
	for i := range selectors {
		if selectors[i].Type != SelectorCommandMatch || selectors[i].Exe == "" || selectors[i].User == "" {
			continue
		}
		if d.matches(&selectors[i], id, resolve) {
			return toProcess(id, selectors[i].Name, sourceCommand), true
		}
	}
	return Process{}, false
}

// matches reports whether a process satisfies a command selector. Every
// configured field is ANDed. Exe is matched by exact resolved /proc/<pid>/exe;
// cmd is an explicit regex over argv used only to narrow discovery for shared
// binaries, not to authorize signaling.
func (d Discoverer) matches(sel *Selector, id Identity, resolve UserResolver) bool {
	// At least one process-shape matcher is required; a selector is never user/group-only
	// (so a bare owner can never select unrelated processes).
	if sel.Exe == "" && sel.Cmd == "" {
		return false
	}
	if sel.Exe != "" {
		// Fail-safe exe match: exact resolved /proc/<pid>/exe, never cmdline.
		exePath := sel.exePath
		if exePath == "" {
			exePath = canonicalizePath(sel.Exe)
		}
		if !id.ExeOK || exePath != id.Exe {
			return false
		}
	}
	if sel.Cmd != "" {
		re := sel.cmdRe
		if re == nil {
			var err error
			if re, err = regexp.Compile(sel.Cmd); err != nil {
				return false
			}
		}
		if !re.MatchString(strings.Join(id.Cmdline, " ")) {
			return false
		}
	}
	if sel.User != "" {
		uid, ok := resolve(sel.User)
		if !ok || uid != id.UID {
			return false
		}
	}
	if sel.Group != "" {
		groupResolve := d.resolveGroup()
		gid, ok := groupResolve(sel.Group)
		if !ok || gid != id.GID {
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
		Group:   id.Group,
		GID:     id.GID,
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

// snapshotIdentities reads every visible process identity. When the reader can
// supply a whole snapshot in one call (the shared CachingReader), that single
// walk is reused across concurrent discoveries; otherwise it falls back to a
// per-PID read.
func snapshotIdentities(reader Reader) map[int]Identity {
	if sr, ok := reader.(SnapshotReader); ok {
		return sr.Snapshot()
	}
	return buildSnapshot(reader)
}

// buildSnapshot walks /proc once via the reader, reading each PID's identity.
func buildSnapshot(reader Reader) map[int]Identity {
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

// ReadPidfile reads the first PID line from a pidfile. Most pidfiles contain
// only that line; PostgreSQL's postmaster.pid keeps the PID on line one and
// cluster metadata below it.
func ReadPidfile(path string) (int, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return 0, fmt.Errorf("read pidfile %s: %w", path, err)
	}
	text := strings.TrimSpace(string(data))
	line, _, _ := strings.Cut(text, procLineSeparator)
	line = strings.TrimSpace(line)
	pid, err := strconv.Atoi(line)
	if err != nil {
		return 0, fmt.Errorf("invalid pid %q", line)
	}
	if pid <= 0 {
		return 0, fmt.Errorf("invalid pid %d", pid)
	}
	return pid, nil
}

// ParseSelectors extracts typed process selectors from a resolved service tree.
// Top-level `pidfile:` becomes one internal pidfile selector. `pidfiles:`
// becomes one pidfile selector per process role. Public `processes:` entries
// are command-match selectors and use exe/cmd directly.
func ParseSelectors(tree map[string]any) ([]Selector, []string) {
	var selectors []Selector
	if paths := cfgval.StringList(tree[ServiceKeyPidfile]); len(paths) > 0 {
		selectors = append(selectors, Selector{
			Name:  string(SelectorPidfile),
			Type:  SelectorPidfile,
			Paths: paths,
		})
	}
	if pidfiles, ok := tree[ServiceKeyPidfiles].(map[string]any); ok {
		for _, role := range sortedMapKeys(pidfiles) {
			paths := cfgval.StringList(pidfiles[role])
			if len(paths) == 0 {
				continue
			}
			selectors = append(selectors, Selector{
				Name:  role,
				Type:  SelectorPidfile,
				Paths: paths,
			})
		}
	}

	raw, ok := tree[SectionProcesses].(map[string]any)
	if !ok {
		return selectors, nil
	}

	var warnings []string
	for _, name := range sortedMapKeys(raw) {
		entry, ok := raw[name].(map[string]any)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("process selector %q is not a mapping", name))
			continue
		}
		sel := Selector{
			Name:  name,
			Type:  SelectorCommandMatch,
			Exe:   cfgval.AsString(entry[SelectorKeyExe]),
			Cmd:   cfgval.AsString(entry[SelectorKeyCmd]),
			User:  cfgval.AsString(entry[SelectorKeyUser]),
			Group: cfgval.AsString(entry[SelectorKeyGroup]),
		}
		if sel.Exe != "" {
			sel.exePath = canonicalizePath(sel.Exe)
		}
		if sel.Exe == "" && sel.Cmd == "" {
			warnings = append(warnings, fmt.Sprintf("process selector %q requires exe or cmd", name))
			continue
		}
		if sel.Cmd != "" {
			re, err := regexp.Compile(sel.Cmd)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("process selector %q has an invalid cmd regex: %v", name, err))
				continue
			}
			sel.cmdRe = re
		}
		selectors = append(selectors, sel)
	}
	return selectors, warnings
}

func sortedMapKeys(m map[string]any) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
