package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/hostfs"
	"sermo/internal/logind"
	"sermo/internal/pkgdb"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

// Reasons an unowned_processes watch lists a process for. They are
// presentation-safe: a PID, a resolved executable and one of these.
const (
	unownedReasonNoUnit        = "outside every init unit"
	unownedReasonClosedSession = "outlived its closed login session"
	unownedReasonUnpackaged    = "executable belongs to no installed package"
	unownedReasonSeparator     = "; "

	unownedKillReasonUnresolvedExe = "executable is unresolved"
	unownedKillReasonReplacedExe   = "executable was replaced or removed on disk"
	unownedKillReasonNoStartTime   = "process start time is unreadable"
	unownedKillReasonProtected     = "init or kernel process"

	unownedAttributionAvailable   = "available"
	unownedAttributionUnavailable = "unavailable"

	unownedCaveatNoAttribution = "init unit attribution unavailable"
	unownedCaveatNoPackageDB   = "no package database"
	unownedCaveatInitNamespace = "init mount namespace unreadable: unit and package criteria off"
	unownedCaveatNoSessions    = "logind session records unreadable: closed-session criterion off"
	unownedCaveatNamespaceFmt  = "%d with unreadable mount namespace: unit and package criteria off"
	unownedCaveatUnresolved    = "unresolved users: "
	unownedCaveatSeparator     = "; "

	defaultUnownedMinAge = 5 * time.Minute
	procNSMountFormat    = "/proc/%d/ns/mnt"
	initPID              = 1
)

// PackageOwner answers whether an executable belongs to an installed package.
// pkgdb.Index implements it; tests inject a fake.
type PackageOwner interface {
	Refresh(ctx context.Context) error
	OwnedAll(ctx context.Context, exes []string) map[string]pkgdb.Ownership
	Backend() pkgdb.Backend
}

var defaultPackageIndex struct {
	once  sync.Once
	index *pkgdb.Index
}

// packageIndexFromDeps returns the shared package index: the configured one,
// or one default instance for the process so the daemon cycle and the web
// backend never parse the package database twice.
func packageIndexFromDeps(deps Deps) PackageOwner { //nolint:ireturn // the configured owner may be any implementation; the fallback is the one shared index
	if deps.PackageIndex != nil {
		return deps.PackageIndex
	}
	defaultPackageIndex.once.Do(func() {
		defaultPackageIndex.index = pkgdb.New(pkgdb.Options{Runner: deps.ExecxRunner})
	})
	return defaultPackageIndex.index
}

// unownedRuntime is what classification needs from the host: the process
// sample, user resolution, the init backend, the package index and the few
// procfs/runtime reads it makes. Every read is injectable.
type unownedRuntime struct {
	sampler     ProcSampler
	resolve     process.UserResolver
	backend     servicemgr.Backend
	packages    PackageOwner
	readFile    func(string) ([]byte, error)
	readDir     func(string) ([]os.DirEntry, error)
	readLink    func(string) (string, error)
	sessionsDir string
}

func unownedRuntimeFromDeps(deps Deps) unownedRuntime {
	var resolve process.UserResolver
	if deps.UserLookup != nil {
		resolve = deps.UserLookup.ResolveUser
	}
	return unownedRuntime{sampler: procSamplerFromDeps(deps), resolve: resolve, backend: deps.Backend, packages: packageIndexFromDeps(deps)}.withDefaults()
}

func (rt unownedRuntime) withDefaults() unownedRuntime {
	if rt.readFile == nil {
		rt.readFile = hostfs.ReadFile
	}
	if rt.readDir == nil {
		rt.readDir = hostfs.ReadDir
	}
	if rt.readLink == nil {
		rt.readLink = hostfs.Readlink
	}
	if rt.resolve == nil {
		rt.resolve = process.DefaultUserLookup().ResolveUser
	}
	return rt
}

// unownedClassifier decides which sampled processes are unowned: outside every
// init unit (by control group) or running an executable no installed package
// owns. It holds no per-cycle state, so the daemon watch and the dashboard's
// manual kill can each build one from the same check entry and agree.
type unownedClassifier struct {
	minAge time.Duration
	users  []string
	ignore []processIdentityRule // exact executables the watch leaves alone; cmd only narrows
	rt     unownedRuntime
}

func newUnownedClassifier(check map[string]any, rt unownedRuntime) (*unownedClassifier, error) {
	c := &unownedClassifier{minAge: defaultUnownedMinAge, rt: rt.withDefaults()}
	if raw, present := check[checks.CheckKeyMinAge]; present {
		c.minAge = cfgval.Duration(raw)
		if c.minAge <= 0 {
			return nil, errors.New("unowned_processes min_age must be a positive duration")
		}
	}
	if raw, present := check[checks.CheckKeyUsers]; present {
		users, err := cfgval.StrictStringList(raw)
		if err != nil || len(users) == 0 {
			return nil, errors.New("unowned_processes users must be a non-empty list")
		}
		c.users = users
	}
	ignoreRules, issues := config.ParseProcessIdentityRules(check[checks.CheckKeyIgnore], checks.CheckTypeUnownedProcesses, checks.CheckKeyIgnore, false)
	if len(issues) > 0 {
		return nil, issues[0]
	}
	ignore, err := newProcessIdentityRules(ignoreRules, "", "unowned_processes ignore")
	if err != nil {
		return nil, err
	}
	c.ignore = ignore
	return c, nil
}

// unownedFinding is one unowned process and why.
type unownedFinding struct {
	info    ProcInfo
	reasons []string
}

func (f unownedFinding) reason() string { return strings.Join(f.reasons, unownedReasonSeparator) }

// unownedScan is one cycle's classification of the whole sample.
type unownedScan struct {
	scanned         int
	findings        []unownedFinding
	unitAttribution bool
	initNamespace   bool // PID 1's mount namespace could be read: the criteria apply
	sessionRecords  bool // logind's session directory is readable: closed sessions can be told
	unreadableNS    int  // candidates whose own mount namespace could not be read
	unresolvedUsers []string
	packageDB       pkgdb.Backend
	packageError    string
}

// usersUnresolved reports whether a declared users filter resolved nothing:
// the scan then excluded every process and proves nothing about the host.
func (s unownedScan) usersUnresolved(declared int) bool {
	return declared > 0 && len(s.unresolvedUsers) == declared
}

func (s unownedScan) attribution() string {
	if s.unitAttribution {
		return unownedAttributionAvailable
	}
	return unownedAttributionUnavailable
}

// scan samples the host and classifies it. ok is false when the process list
// could not be read, which a caller must not mistake for an empty host.
func (c *unownedClassifier) scan(ctx context.Context, now time.Time) (unownedScan, bool) {
	samples, ok := c.rt.sampler.Sample(ProcMatch{All: true})
	if !ok {
		return unownedScan{}, false
	}
	return c.classify(ctx, samples, now), true
}

// current re-samples one PID and returns its sample only while it is still
// classified unowned: the population check a kill requires before trusting a
// PID number, fresh or after a grace period. Classification is per process, so
// the one PID is sampled and judged alone rather than the whole host re-read.
func (c *unownedClassifier) current(ctx context.Context, now time.Time, pid int) (ProcInfo, bool) {
	samples, ok := c.rt.sampler.Sample(ProcMatch{PID: pid})
	if !ok {
		return ProcInfo{}, false
	}
	for i := range samples {
		if samples[i].PID != pid {
			continue // a sampler that ignores the selector still answers for this PID only
		}
		scan := c.classify(ctx, samples[i:i+1], now)
		if len(scan.findings) == 1 {
			return scan.findings[0].info, true
		}
		return ProcInfo{}, false
	}
	return ProcInfo{}, false
}

// classify applies the exclusions, then the unit criterion and the package
// criterion, to every sampled process. Anything it cannot tell is not a
// finding: absence of evidence never lists a process.
func (c *unownedClassifier) classify(ctx context.Context, samples []ProcInfo, now time.Time) unownedScan {
	scan := unownedScan{unitAttribution: c.unitAttributionAvailable(), packageDB: pkgdb.BackendNone}
	if c.rt.packages != nil {
		if err := c.rt.packages.Refresh(ctx); err != nil {
			scan.packageError = err.Error()
		}
		scan.packageDB = c.rt.packages.Backend()
	}
	uids, unresolved := c.resolveUsers()
	scan.unresolvedUsers = unresolved
	initNS, initNSOK := c.mountNamespace(initPID)
	scan.initNamespace = initNSOK
	scan.sessionRecords = c.rt.backend != servicemgr.BackendSystemd || logind.SessionsReadable(c.rt.readDir, c.rt.sessionsDir)
	// Share each session verdict within this sample only. A later scan or kill
	// revalidation must read logind's current record again.
	closedSessions := map[string]bool{}
	// candidates are the processes no init unit accounts for: the only ones the
	// package criterion judges. A unit's member is owned whatever its binary —
	// a service's own plugin helpers, or a daemon installed outside the package
	// manager, are that service's processes, not strays.
	candidates := make([]ProcInfo, 0, len(samples))
	unitReasons := make([]string, 0, len(samples))
	onHost := make([]bool, 0, len(samples))
	for i := range samples {
		if ctx.Err() != nil {
			return scan
		}
		if c.excluded(samples[i], now, uids) {
			continue
		}
		scan.scanned++
		owner := servicemgr.ClassifyCgroup(samples[i].Cgroup)
		if unitOwned(owner.Class) {
			continue
		}
		// A process in another mount namespace than PID 1 is not the host's: a
		// container under a cgroupfs driver sits in /docker/<id>, /kubepods/… or
		// /lxc/<name>, a path that names no host unit yet is no stray either, and
		// its /usr/bin is not the host's. Only a process confirmed in init's
		// namespace is judged; an unreadable namespace judges nothing.
		host, readable := c.inMountNamespace(samples[i].PID, initNS, initNSOK)
		if !readable {
			scan.unreadableNS++
		}
		candidates = append(candidates, samples[i])
		onHost = append(onHost, host)
		unitReasons = append(unitReasons, c.unitReason(owner, scan, host, closedSessions))
	}
	packaged, judged := c.packageOwnership(ctx, candidates, onHost)
	for i := range candidates {
		finding := unownedFinding{info: candidates[i]}
		if unitReasons[i] != "" {
			finding.reasons = append(finding.reasons, unitReasons[i])
		}
		if judged[i] && packaged[ownershipExecutable(candidates[i])] == pkgdb.NotOwned {
			finding.reasons = append(finding.reasons, unownedReasonUnpackaged)
		}
		if len(finding.reasons) > 0 {
			scan.findings = append(scan.findings, finding)
		}
	}
	slices.SortFunc(scan.findings, func(a, b unownedFinding) int { return cmp.Compare(a.info.PID, b.info.PID) })
	return scan
}

// unitAttributionAvailable reports whether root-group membership means
// anything on this host: systemd always places processes in units; OpenRC
// only with per-service cgroups enabled.
func (c *unownedClassifier) unitAttributionAvailable() bool {
	switch c.rt.backend {
	case servicemgr.BackendSystemd:
		return true
	case servicemgr.BackendOpenRC:
		return servicemgr.OpenRCUnitCgroupsPresent(c.rt.readDir)
	default:
		return false
	}
}

// resolveUsers maps the declared users filter to real uids. A name that does
// not resolve is returned so the scan can say which accounts it is blind to: a
// filter that resolves nothing excludes every process, and a clean result over
// nothing must never pass for a clean host.
func (c *unownedClassifier) resolveUsers() (map[uint32]bool, []string) {
	if len(c.users) == 0 {
		return nil, nil
	}
	uids := make(map[uint32]bool, len(c.users))
	var unresolved []string
	for _, user := range c.users {
		if uid, ok := c.rt.resolve(user); ok {
			uids[uid] = true
		} else {
			unresolved = append(unresolved, user)
		}
	}
	return uids, unresolved
}

// excluded applies the exclusions that keep a process out of the scan: init
// and kernel threads, zombies, processes younger than min_age (or whose age
// cannot be read), accounts outside users, and ignore rules.
func (c *unownedClassifier) excluded(info ProcInfo, now time.Time, uids map[uint32]bool) bool {
	if process.ProtectedIdentity(info.Identity) || info.State == process.ProcStateZombie {
		return true
	}
	if info.StartTime.IsZero() || now.Sub(info.StartTime) < c.minAge {
		return true
	}
	if uids != nil && !uids[info.UID] {
		return true
	}
	for _, rule := range c.ignore {
		if _, ignored := rule.match(info, c.rt.resolve); ignored {
			return true
		}
	}
	return false
}

// unitOwned reports whether a control group class means an init unit
// accounts for the process: a service, a transient scope, init itself or a
// user manager. Everything else — the root group, a login session, an
// unreadable record — is the territory the watch judges.
func unitOwned(class servicemgr.CgroupClass) bool {
	switch class {
	case servicemgr.CgroupClassService, servicemgr.CgroupClassScope, servicemgr.CgroupClassInit, servicemgr.CgroupClassUserManager:
		return true
	case servicemgr.CgroupClassRoot, servicemgr.CgroupClassSlice, servicemgr.CgroupClassSession, servicemgr.CgroupClassUnknown:
	}
	return false
}

// unitReason applies the control-group criterion to a process no unit owns.
// Root-group or bare-slice membership is evidence only for a process confirmed
// in the host's mount namespace (onHost): a container's cgroup path says
// nothing about host units. A closed login session is logind's own verdict and
// needs no such confirmation, but only while logind's records can be read at
// all: a missing directory would otherwise call every session closed.
func (c *unownedClassifier) unitReason(owner servicemgr.CgroupOwner, scan unownedScan, onHost bool, closedSessions map[string]bool) string {
	switch owner.Class {
	case servicemgr.CgroupClassRoot, servicemgr.CgroupClassSlice:
		if scan.unitAttribution && onHost {
			return unownedReasonNoUnit
		}
	case servicemgr.CgroupClassSession:
		if c.rt.backend != servicemgr.BackendSystemd || !scan.sessionRecords {
			return ""
		}
		closed, sampled := closedSessions[owner.Owner]
		if !sampled {
			closed = logind.SessionClosed(c.rt.readFile, c.rt.sessionsDir, owner.Owner)
			closedSessions[owner.Owner] = closed
		}
		if closed {
			return unownedReasonClosedSession
		}
	case servicemgr.CgroupClassService, servicemgr.CgroupClassScope, servicemgr.CgroupClassInit,
		servicemgr.CgroupClassUserManager, servicemgr.CgroupClassUnknown:
	}
	return ""
}

// ownershipExecutable is the path the package criterion looks up: the
// resolved executable, or the replaced one's previous path.
func ownershipExecutable(info ProcInfo) string {
	if info.ExeOK && info.Exe != "" {
		return info.Exe
	}
	return info.ExePrev
}

// packageOwnership looks every candidate executable up at once and reports,
// per candidate, whether the answer applies to it: only a candidate confirmed
// in the host's mount namespace (onHost) is judged, since a container runs a
// /usr/bin that is not the host's and the host database cannot judge it even
// when another process shares the same path.
func (c *unownedClassifier) packageOwnership(ctx context.Context, candidates []ProcInfo, onHost []bool) (map[string]pkgdb.Ownership, []bool) {
	judged := make([]bool, len(candidates))
	if c.rt.packages == nil {
		return nil, judged
	}
	seen := map[string]bool{}
	var exes []string
	for i := range candidates {
		exe := ownershipExecutable(candidates[i])
		if exe == "" || !onHost[i] {
			continue
		}
		judged[i] = true
		if !seen[exe] {
			seen[exe] = true
			exes = append(exes, exe)
		}
	}
	if len(exes) == 0 {
		return nil, judged
	}
	return c.rt.packages.OwnedAll(ctx, exes), judged
}

func (c *unownedClassifier) mountNamespace(pid int) (string, bool) {
	ns, err := c.rt.readLink(fmt.Sprintf(procNSMountFormat, pid))
	if err != nil || ns == "" {
		return "", false
	}
	return ns, true
}

// inMountNamespace reports whether pid's mount namespace is ns, and whether
// the question could be answered at all. An unreadable link (another user's
// process under an unprivileged daemon, a process that just exited) is not
// membership, and is counted so the scan can say how many it could not judge.
func (c *unownedClassifier) inMountNamespace(pid int, ns string, nsOK bool) (inNS, readable bool) {
	if !nsOK {
		return false, true // nothing to compare against; init's own caveat covers it
	}
	got, ok := c.mountNamespace(pid)
	return ok && got == ns, ok
}

// manualKillable reports whether the dashboard may signal this finding: it
// needs the exact identity the kill gate requires. The reason is shown next to
// a disabled button.
func manualKillable(info ProcInfo) (bool, string) {
	switch {
	case process.ProtectedIdentity(info.Identity):
		return false, unownedKillReasonProtected
	case !info.ExeOK && info.ExePrev != "":
		return false, unownedKillReasonReplacedExe
	case !info.ExeOK:
		return false, unownedKillReasonUnresolvedExe
	case info.StartTicks == 0:
		return false, unownedKillReasonNoStartTime
	default:
		return true, ""
	}
}
