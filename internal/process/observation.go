package process

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

// Observation is one fresh, error-aware view of a service's processes. An empty
// best-effort discovery is not proof of absence: only AbsenceKnown permits an
// operation to reconcile an active init marker without a live daemon.
type Observation struct {
	Processes []Process
	Warnings  []string
	Trusted   bool
	// ReplacedExecutable permits stopping a daemon after a package upgrade,
	// but cannot verify a successful start or a replacement generation.
	ReplacedExecutable bool
	AbsenceKnown       bool
	IdentityRequired   bool
	// snapshot retains host-wide generation evidence independently of the
	// current unit's attributed tree. It is immutable and excludes zombies.
	snapshot map[int]Identity
}

// PrincipalIdentity is how a service's discovered processes relate to its
// strict principal identity (exact executable and real user). One rule serves
// the operation's fresh observation and the monitor's cycle; on the monitor it
// is only a forecast and authorizes nothing.
type PrincipalIdentity struct {
	// Attributed reports at least one non-delegated process.
	Attributed bool
	// Required reports a strict principal selector to judge them by.
	Required bool
	// Trusted reports a process that matches that identity now.
	Trusted bool
	// ReplacedExecutable reports a process that would match it but for its
	// binary having been replaced on disk. It is judged only while no process
	// is trusted: a trusted daemon needs no replaced-binary evidence.
	ReplacedExecutable bool
}

// Unverified reports processes that cannot be tied to the service's own
// identity: an operation refuses to treat such an active service as its daemon.
func (p PrincipalIdentity) Unverified() bool {
	return p.Attributed && p.Required && !p.Trusted && !p.ReplacedExecutable
}

// Principal returns the observation's identity evidence.
func (o Observation) Principal() PrincipalIdentity {
	return PrincipalIdentity{
		Attributed:         hasNonDelegated(o.Processes),
		Required:           o.IdentityRequired,
		Trusted:            o.Trusted,
		ReplacedExecutable: o.ReplacedExecutable,
	}
}

// PrincipalIdentityIn judges the processes a monitoring cycle already
// discovered by the rule operations apply to a fresh observation, reading the
// shared snapshot instead of rescanning /proc. Deleted-executable candidates
// outside the attributed tree, which an operation always adds, are looked up
// only when the attributed processes alone neither trust the service nor show
// its replaced binary, so a healthy service costs one match. A process that
// left the snapshot since the cycle's discovery is no evidence at all. It is a
// forecast for reporting: an operation always observes afresh.
func (d Discoverer) PrincipalIdentityIn(attributed []Process, selectors []Selector) PrincipalIdentity {
	if !HasStrictPrincipal(selectors) {
		return PrincipalIdentity{Attributed: hasNonDelegated(attributed)}
	}
	out := d.principalIdentity(attributed, selectors)
	if !out.Trusted && !out.ReplacedExecutable {
		out = d.principalIdentity(d.addDeletedCandidates(slices.Clone(attributed), selectors), selectors)
	}
	return out
}

// principalIdentity narrows the evidence to the strict main selector when one
// exists, so a worker cannot stand in for the main process. An external
// candidate can show a replaced binary but never trust. The first trusted
// process settles the verdict.
func (d Discoverer) principalIdentity(procs []Process, selectors []Selector) PrincipalIdentity {
	identities := selectors
	for _, sel := range selectors {
		if sel.Name == RoleMain && sel.IsStrictPrincipal() {
			identities = []Selector{sel}
			break
		}
	}
	out := PrincipalIdentity{Required: HasStrictPrincipal(identities)}
	reader, resolve := d.reader(), d.resolveUser()
	for _, proc := range procs {
		if proc.Delegated {
			continue
		}
		id, ok := reader.Identity(proc.PID)
		if !ok {
			continue
		}
		out.Attributed = true
		if proc.Stray {
			continue
		}
		for i := range identities {
			sel := &identities[i]
			if !proc.External && sel.HasStrictIdentity() && d.matches(sel, id, resolve) {
				out.Trusted = true
				return out
			}
			if sel.IsStrictPrincipal() && d.matchesDeletedExe(sel, id, resolve) {
				out.ReplacedExecutable = true
			}
		}
	}
	return out
}

func hasNonDelegated(procs []Process) bool {
	return slices.ContainsFunc(procs, func(p Process) bool { return !p.Delegated })
}

// ReasonTrackedOutsideDiscovery refuses a signal to a survivor that discovery no
// longer attributes to the service: losing attribution is not evidence of exit,
// but it is not proof of ownership either.
const ReasonTrackedOutsideDiscovery = "tracked process remains outside service discovery"

// RetainSurvivors follows old generations across selector/cgroup changes.
// Losing attribution is not evidence of exit. Changed identities stay visible
// but lose signal authority, so neither escalation nor start can hide them.
func (o Observation) RetainSurvivors(previous []Process) []Process {
	procs := slices.Clone(o.Processes)
	for _, old := range previous {
		id, alive := o.snapshot[old.PID]
		if !alive || old.Delegated {
			continue
		}
		if old.StartTicks != 0 && id.StartTicksOK && id.StartTicks != old.StartTicks {
			continue
		}
		index := slices.IndexFunc(procs, func(p Process) bool { return p.PID == old.PID })
		if index < 0 {
			p := toProcess(id, old.Role, old.Source)
			p.SignalBlockReason = ReasonTrackedOutsideDiscovery
			procs = append(procs, p)
			continue
		}
		if old.UID != id.UID || old.Cgroup != id.Cgroup ||
			old.signalExecutable() != procs[index].signalExecutable() ||
			old.ExeFile.Inode != 0 && old.ExeFile != id.ExeFile {
			procs[index].SignalBlockReason = "tracked process identity changed during cleanup"
		}
		if old.SignalBlockReason != "" {
			procs[index].SignalBlockReason = old.SignalBlockReason
		}
	}
	return procs
}

// VerifyExited checks old generations against the complete observed process
// table. Leaving the service's cgroup or parent tree is not proof of exit.
func (o Observation) VerifyExited(procs []Process) error {
	if o.snapshot == nil {
		return errors.New("missing process snapshot to verify old generations exited")
	}
	for _, proc := range procs {
		if proc.StartTicks == 0 {
			return fmt.Errorf("unknown generation before stop for pid %d", proc.PID)
		}
		id, present := o.snapshot[proc.PID]
		if !present {
			continue
		}
		if !id.StartTicksOK || id.StartTicks == 0 {
			return fmt.Errorf("cannot verify current generation for pid %d", proc.PID)
		}
		if id.StartTicks == proc.StartTicks {
			return fmt.Errorf("old process generation for pid %d remains after stop", proc.PID)
		}
	}
	return nil
}

// Observe reuses discovery and matching against one immutable snapshot. It does
// not signal processes or mutate the init state.
func (d Discoverer) Observe(selectors []Selector) (Observation, error) {
	return d.observe(selectors, false, true)
}

// ObserveCached shares the monitor's bounded-freshness snapshot. It is for
// read-only inventory; consumers must revalidate live identities before using
// the result as evidence. Operations always use Observe or ObserveTracked.
func (d Discoverer) ObserveCached(selectors []Selector) (Observation, error) {
	return d.observe(selectors, false, false)
}

// ObserveTracked retains a complete snapshot while following old generations,
// even when the backend stops reporting PIDs and no selectors are configured.
// It does not turn loss of backend attribution into proof of process exit.
func (d Discoverer) ObserveTracked(selectors []Selector, previous []Process) (Observation, error) {
	out, err := d.observe(selectors, len(previous) > 0, true)
	if err != nil {
		return out, err
	}
	out.Processes = out.RetainSurvivors(previous)
	return out, nil
}

func (d Discoverer) observe(selectors []Selector, requireSnapshot, fresh bool) (Observation, error) {
	backendPIDs := d.BackendPIDs
	var backendErr error
	if fresh && d.FreshBackendPIDs != nil {
		var pids []int
		pids, backendErr = d.FreshBackendPIDs()
		backendPIDs = func() []int { return pids }
	}
	backend := backendPIDSeeds(backendPIDs)
	if len(selectors) == 0 && len(backend) == 0 && !requireSnapshot && backendErr == nil {
		return Observation{}, nil
	}
	reader := d.reader()
	if inv, ok := reader.(interface{ Invalidate() }); ok && fresh {
		inv.Invalidate()
	}
	snapshot, err := Snapshot(reader)
	if err != nil {
		return Observation{}, fmt.Errorf("observe processes: %w", err)
	}
	snapshot = withoutZombieIdentities(snapshot)
	frozen := d
	frozen.BackendPIDs, frozen.FreshBackendPIDs = func() []int { return backend }, nil
	frozen.Reader = observationReader{idx: buildSnapshotIndex(snapshot)}
	procs, warnings := frozen.Discover(selectors)
	if backendErr != nil {
		warnings = append(warnings, fmt.Sprintf("backend PID query: %v", backendErr))
	}
	out := Observation{snapshot: snapshot, Processes: procs}
	// ReplacedExecutable is diagnostic evidence for a backend stop, not signal
	// authority. The added candidates need independent verification for cleanup.
	out.Processes = frozen.addDeletedCandidates(out.Processes, selectors)
	out.Warnings = warnings
	principal := frozen.principalIdentity(out.Processes, selectors)
	out.IdentityRequired, out.Trusted, out.ReplacedExecutable = principal.Required, principal.Trusted, principal.ReplacedExecutable
	out.AbsenceKnown = len(UncertainWarnings(warnings)) == 0 && frozen.absenceKnown(selectors, snapshot)
	if !out.Trusted && !out.ReplacedExecutable && len(UncertainWarnings(warnings)) > 0 {
		return out, fmt.Errorf("runtime discovery: %s", strings.Join(UncertainWarnings(warnings), "; "))
	}
	return out, nil
}

// A zombie named by a backend or pidfile cannot anchor discovery to its dead
// process tree: that would hide a live daemon from the selector fallback. Clone
// only when filtering is necessary so a shared monitoring snapshot stays intact.
func withoutZombieIdentities(snapshot map[int]Identity) map[int]Identity {
	for _, id := range snapshot {
		if id.State == ProcStateZombie {
			live := maps.Clone(snapshot)
			maps.DeleteFunc(live, func(_ int, id Identity) bool { return id.State == ProcStateZombie })
			return live
		}
	}
	return snapshot
}

func (d Discoverer) absenceKnown(selectors []Selector, snapshot map[int]Identity) bool {
	hasIdentity := false
	for i := range selectors {
		sel := &selectors[i]
		if sel.Delegated || !sel.HasStrictIdentity() {
			continue
		}
		uid, ok := d.resolveUser()(sel.User)
		if !ok || !filepath.IsAbs(sel.Exe) {
			return false
		}
		hasIdentity = true
		for _, id := range snapshot {
			if id.UID == uid && !id.ExeOK && id.ExePrev == "" && id.State != ProcStateZombie &&
				!protectedKernelProcess(id.PID, id.PPID, id.ExeOK, id.Cmdline) {
				return false
			}
		}
	}
	return hasIdentity
}

type observationReader struct{ idx *snapshotIndex }

func (r observationReader) PIDs() ([]int, error) { return r.idx.sorted, nil }
func (r observationReader) Identity(pid int) (Identity, bool) {
	id, ok := r.idx.byPID[pid]
	return id, ok
}
func (r observationReader) Snapshot() map[int]Identity    { return r.idx.byPID }
func (r observationReader) snapshotIndex() *snapshotIndex { return r.idx }
