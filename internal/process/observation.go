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
			p.SignalBlockReason = "tracked process remains outside service discovery"
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
	return d.observe(selectors, false)
}

// ObserveTracked retains a complete snapshot while following old generations,
// even when the backend stops reporting PIDs and no selectors are configured.
// It does not turn loss of backend attribution into proof of process exit.
func (d Discoverer) ObserveTracked(selectors []Selector, previous []Process) (Observation, error) {
	out, err := d.observe(selectors, len(previous) > 0)
	if err != nil {
		return out, err
	}
	out.Processes = out.RetainSurvivors(previous)
	return out, nil
}

func (d Discoverer) observe(selectors []Selector, requireSnapshot bool) (Observation, error) {
	backend := backendPIDSeeds(d.BackendPIDs)
	if len(selectors) == 0 && len(backend) == 0 && !requireSnapshot {
		return Observation{}, nil
	}
	reader := d.reader()
	if inv, ok := reader.(interface{ Invalidate() }); ok {
		inv.Invalidate()
	}
	snapshot, err := Snapshot(reader)
	if err != nil {
		return Observation{}, fmt.Errorf("observe processes: %w", err)
	}
	snapshot = withoutZombieIdentities(snapshot)
	frozen := d
	frozen.BackendPIDs = func() []int { return backend }
	frozen.Reader = observationReader{idx: buildSnapshotIndex(snapshot)}
	procs, warnings := frozen.Discover(selectors)
	identities := selectors
	for _, sel := range selectors {
		if sel.Name == RoleMain && !sel.Delegated && sel.HasStrictIdentity() {
			identities = []Selector{sel}
			break
		}
	}
	out := Observation{snapshot: snapshot, Processes: procs, IdentityRequired: slices.ContainsFunc(identities, func(s Selector) bool { return !s.Delegated && s.HasStrictIdentity() })}
	// ReplacedExecutable is diagnostic evidence for a backend stop, not signal
	// authority. The added candidates need independent verification for cleanup.
	out.Processes = frozen.addDeletedCandidates(out.Processes, selectors)
	out.Warnings = warnings
	for _, proc := range out.Processes {
		if proc.Delegated || proc.Stray {
			continue
		}
		if _, ok := frozen.StrictMatchPID(proc.PID, identities); ok && !proc.External {
			out.Trusted = true
		}
		for i := range identities {
			sel := &identities[i]
			if !sel.Delegated && sel.HasStrictIdentity() && frozen.matchesDeletedExe(sel, snapshot[proc.PID], frozen.resolveUser()) {
				out.ReplacedExecutable = true
			}
		}
	}
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
