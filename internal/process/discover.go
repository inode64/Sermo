package process

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sermo/internal/cfgval"
	"slices"
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
	idx := snapshotIndexFor(reader)
	snapshot := idx.byPID

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
	for _, pid := range idx.sorted {
		id := snapshot[pid]
		for i := range selectors {
			if selectors[i].Type == SelectorCommandMatch && d.matches(&selectors[i], id, resolve) {
				add(id, selectors[i].Name, sourceCommand)
				break
			}
		}
	}

	// 3. descendants from the process tree.
	for _, pid := range descendants(idx.children, order) {
		add(snapshot[pid], RoleChild, sourceChild)
	}

	// 4. delegated marking. The helper keeps the common no-delegation case on a
	// cheap fast path and owns the extra identity/tree work for the few services
	// that need it.
	d.markDelegated(selectors, found, idx, resolve)

	result := make([]Process, 0, len(order))
	for _, pid := range order {
		result = append(result, found[pid])
	}
	return result, warnings
}

// markDelegated marks processes matched by delegated selectors and propagates
// that ownership down their child trees. Most services declare no such selector,
// so they return after one selector scan without extra process matching, maps or
// tree traversal.
func (d Discoverer) markDelegated(selectors []Selector, found map[int]Process, idx *snapshotIndex, resolve UserResolver) {
	var delegatedSelectors []Selector
	for _, selector := range selectors {
		if selector.Delegated {
			delegatedSelectors = append(delegatedSelectors, selector)
		}
	}
	if len(delegatedSelectors) == 0 {
		return
	}

	// Resolve by identity rather than by role: a backend seed labels every PID
	// in the unit's cgroup Role "main" before command selectors name workload
	// children. Delegation then flows down the tree because a workload owns
	// everything it spawns.
	delegated := map[int]bool{}
	for pid := range found {
		if d.matchesAnyDelegated(delegatedSelectors, idx.byPID[pid], resolve) {
			delegated[pid] = true
		}
	}
	for _, pid := range descendants(idx.children, slices.Sorted(maps.Keys(delegated))) {
		delegated[pid] = true
	}
	for pid := range delegated {
		if proc, ok := found[pid]; ok {
			proc.Delegated = true
			found[pid] = proc
		}
	}
}

// StaleBinary reports one process still running a binary that was replaced or
// removed on disk — almost always a package upgrade without a service restart.
type StaleBinary struct {
	PID  int    // the running process
	Path string // the path the deleted binary occupied
}

// StaleBinariesIn lists the processes of a service whose binary was replaced on
// disk. It covers both ways the condition surfaces: a process the init backend
// or a pidfile already attributed (whose unusable exe is otherwise silent), and
// one an exe selector would have matched but could not, which makes the service
// look like it has no process at all.
//
// It takes the processes discovery already produced for this cycle rather than
// rediscovering them, and reads the snapshot the caller already holds. It is a
// read-only diagnostic: it never widens what Discover selects, so a process
// reported here is still never signalled.
func (d Discoverer) StaleBinariesIn(attributed []Process, selectors []Selector) []StaleBinary {
	var out []StaleBinary
	var seen map[int]bool
	mark := func(pid int) bool {
		if seen[pid] {
			return false
		}
		if seen == nil {
			seen = map[int]bool{}
		}
		seen[pid] = true
		return true
	}
	for _, p := range attributed {
		if p.ExePrev != "" && mark(p.PID) {
			out = append(out, StaleBinary{PID: p.PID, Path: p.ExePrev})
		}
	}
	if !hasExeSelector(selectors) {
		return out
	}

	resolve := d.resolveUser()
	idx := snapshotIndexFor(d.reader())
	for _, pid := range idx.deleted {
		if seen[pid] {
			continue
		}
		id := idx.byPID[pid]
		for i := range selectors {
			if selectors[i].Type == SelectorCommandMatch && d.matchesDeletedExe(&selectors[i], id, resolve) {
				mark(pid)
				out = append(out, StaleBinary{PID: pid, Path: id.ExePrev})
				break
			}
		}
	}
	return out
}

// StaleBinaries discovers the service's processes and reports the stale ones.
// Callers that already discovered this cycle should use StaleBinariesIn.
func (d Discoverer) StaleBinaries(selectors []Selector) []StaleBinary {
	attributed, _ := d.Discover(selectors)
	return d.StaleBinariesIn(attributed, selectors)
}

// hasExeSelector reports whether any selector matches on exe, the only kind a
// replaced binary can silently break.
func hasExeSelector(selectors []Selector) bool {
	for i := range selectors {
		if selectors[i].Type == SelectorCommandMatch && selectors[i].Exe != "" {
			return true
		}
	}
	return false
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

// matchesAnyDelegated reports whether any delegated selector claims id. Unlike
// matchesAny it also accepts a process whose binary was replaced on disk, matched
// through ExePrev.
//
// That case is the one that matters most. An unresolvable exe already makes a
// process unkillable, so declining to delegate it is the worst combination there
// is: it stays a residual that nothing may ever signal, and every staged stop
// therefore ends in orphan_processes with the service left stopped. Delegation
// only ever removes authority — it can never make a process signallable — so
// recognizing a stale-binary process here cannot widen what Sermo may signal.
func (d Discoverer) matchesAnyDelegated(selectors []Selector, id Identity, resolve UserResolver) bool {
	if d.matchesAny(selectors, id, resolve) {
		return true
	}
	for i := range selectors {
		if d.matchesDeletedExe(&selectors[i], id, resolve) {
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
		if !strictIdentity(&selectors[i]) {
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
// cmd is an explicit regex over argv that narrows a shared binary down to one
// role. cmd never authorizes signaling on its own — a killable process must
// still match the exact resolved exe and the real UID — but it does narrow the
// identity derived by EnableAutomaticReaping, so a selector that distinguishes
// a daemon from its workload children keeps that distinction when signalling.
func (d Discoverer) matches(sel *Selector, id Identity, resolve UserResolver) bool {
	// At least one process-shape matcher is required; a selector is never user/group-only
	// (so a bare owner can never select unrelated processes).
	if sel.Exe == "" && sel.Cmd == "" {
		return false
	}
	if sel.Exe != "" {
		// Fail-safe exe match: exact resolved /proc/<pid>/exe, never cmdline.
		if !id.ExeOK || selectorExePath(sel) != id.Exe {
			return false
		}
	}
	return d.matchesNonExe(sel, id, resolve)
}

// selectorExePath returns the selector's canonical exe path, canonicalizing
// lazily when ParseSelectors did not.
func selectorExePath(sel *Selector) string {
	if sel.exePath != "" {
		return sel.exePath
	}
	return canonicalizePath(sel.Exe)
}

// strictIdentity reports whether a selector carries an identity strong enough to
// authorize signalling: a command selector with both an exact executable and a
// real user. Safety invariants 5 and 7 are stated in exactly those terms, so the
// derived kill authority and the strict PID match share one definition of it
// rather than restating the predicate apart from each other.
func strictIdentity(sel *Selector) bool {
	return sel.Type == SelectorCommandMatch && sel.Exe != "" && sel.User != ""
}

// selectorCmdRegexp returns the selector's compiled cmd regex, compiling lazily
// when ParseSelectors did not and nil when the selector declares no cmd or the
// pattern does not compile. A nil result means "this selector adds no cmd
// constraint", so callers must treat it as no-match only where cmd was declared.
func selectorCmdRegexp(sel *Selector) *regexp.Regexp {
	if sel.Cmd == "" {
		return nil
	}
	if sel.cmdRe != nil {
		return sel.cmdRe
	}
	re, err := regexp.Compile(sel.Cmd)
	if err != nil {
		return nil
	}
	return re
}

// matchesDeletedExe reports whether sel would have matched id if id's binary had
// not been replaced on disk: the deleted path is exactly the selector's exe and
// every other field matches. It authorizes nothing — matches() still returns
// false for such a process, so it is never selected and never signalled. This
// exists only so a package upgrade that silently breaks discovery can be
// reported instead of surfacing as an unexplained absence of processes.
func (d Discoverer) matchesDeletedExe(sel *Selector, id Identity, resolve UserResolver) bool {
	if sel.Exe == "" || id.ExePrev == "" {
		return false
	}
	if selectorExePath(sel) != id.ExePrev {
		return false
	}
	return d.matchesNonExe(sel, id, resolve)
}

// matchesNonExe applies the cmd, user and group fields, which are shared by the
// live and deleted-exe matchers.
func (d Discoverer) matchesNonExe(sel *Selector, id Identity, resolve UserResolver) bool {
	if sel.Cmd != "" {
		re := selectorCmdRegexp(sel)
		if re == nil || !re.MatchString(strings.Join(id.Cmdline, " ")) {
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

		ExePrev: id.ExePrev,
	}
}

// snapshotIndex pairs an identity snapshot with the derived structures every
// Discover call walks: the sorted PID order (deterministic command_match scan)
// and the parent-to-children map (tree expansion). Building it once per /proc
// walk — instead of once per service discovery — keeps the per-cycle cost flat
// as services multiply.
type snapshotIndex struct {
	byPID    map[int]Identity
	sorted   []int
	children map[int][]int
	// deleted lists the PIDs whose binary was replaced on disk, collected in
	// the pass that builds children. Doing it here means every service shares
	// one scan per snapshot refresh instead of each sweeping all PIDs to find
	// a set that is normally empty.
	deleted []int
}

func buildSnapshotIndex(snapshot map[int]Identity) *snapshotIndex {
	idx := &snapshotIndex{
		byPID:    snapshot,
		sorted:   slices.Sorted(maps.Keys(snapshot)),
		children: map[int][]int{},
	}
	for pid, id := range snapshot {
		idx.children[id.PPID] = append(idx.children[id.PPID], pid)
		if id.ExePrev != "" {
			idx.deleted = append(idx.deleted, pid)
		}
	}
	sort.Ints(idx.deleted)
	for _, kids := range idx.children {
		sort.Ints(kids)
	}
	return idx
}

// indexedSnapshotReader is the optional capability the shared CachingReader
// implements: serve the index built once per snapshot refresh.
type indexedSnapshotReader interface {
	snapshotIndex() *snapshotIndex
}

func snapshotIndexFor(reader Reader) *snapshotIndex {
	if ir, ok := reader.(indexedSnapshotReader); ok {
		return ir.snapshotIndex()
	}
	return buildSnapshotIndex(snapshotIdentities(reader))
}

// descendants returns every PID reachable as a child of the seed PIDs, excluding
// the seeds themselves, in a stable order. children is the prebuilt
// parent-to-children map of the snapshot being walked.
func descendants(children map[int][]int, seeds []int) []int {
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

// snapshotIdentities reads every visible process identity for ordinary process
// discovery. Discovery remains best-effort, so it retains the partial snapshot
// when /proc cannot be listed; safety-sensitive callers use Snapshot directly
// and handle its error.
func snapshotIdentities(reader Reader) map[int]Identity {
	snapshot, _ := Snapshot(reader)
	return snapshot
}

// readSnapshot walks /proc once via the reader, reading each PID's identity.
// Identities that vanish during the walk are omitted; an unreadable PID list is
// returned to callers that must fail closed instead of accepting an empty view.
func readSnapshot(reader Reader) (map[int]Identity, error) {
	snapshot := map[int]Identity{}
	pids, err := reader.PIDs()
	if err != nil {
		return snapshot, fmt.Errorf("list process IDs: %w", err)
	}
	for _, pid := range pids {
		if id, ok := reader.Identity(pid); ok {
			snapshot[pid] = id
		}
	}
	return snapshot, nil
}

// ReadPidfile reads the first PID line from a pidfile. Most pidfiles contain
// only that line; PostgreSQL's postmaster.pid keeps the PID on line one and
// cluster metadata below it.
//
// A negative value is the process-group form some daemons write (DCC's
// dccifd, for example) so that a shutdown script can `kill -- -$(cat pidfile)`
// the whole group. A process group id is always its leader's pid, so the
// absolute value names that leader and discovery uses it like any other
// pidfile pid — it still has to be a live process to match, and the tree walk
// then picks up the rest of the group as children. `-1` and `-0` are refused:
// in kill semantics -1 means every process, so it identifies no service.
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
	if pid < 0 {
		if pid >= -1 {
			return 0, fmt.Errorf("invalid process group %d", pid)
		}
		return -pid, nil
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
		for _, role := range slices.Sorted(maps.Keys(pidfiles)) {
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
	for _, name := range slices.Sorted(maps.Keys(raw)) {
		entry, ok := raw[name].(map[string]any)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("process selector %q is not a mapping", name))
			continue
		}
		delegated := false
		if value, present := entry[SelectorKeyDelegated]; present {
			var valid bool
			delegated, valid = value.(bool)
			if !valid {
				warnings = append(warnings, fmt.Sprintf("process selector %q delegated must be a boolean", name))
				continue
			}
		}
		sel := Selector{
			Name:      name,
			Type:      SelectorCommandMatch,
			Exe:       cfgval.AsString(entry[SelectorKeyExe]),
			Cmd:       cfgval.AsString(entry[SelectorKeyCmd]),
			User:      cfgval.AsString(entry[SelectorKeyUser]),
			Group:     cfgval.AsString(entry[SelectorKeyGroup]),
			Delegated: delegated,
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
