package process

import "slices"

// foreignProcess removes a positively attributed foreign instance from global
// selector fallback. Unknown ownership does not change ordinary discovery.
func (d Discoverer) foreignProcess(id Identity) bool {
	if d.ProcessOwnership == nil {
		return false
	}
	foreign, known := d.ProcessOwnership(id)
	return known && foreign
}

// addDeletedCandidates shares the observation snapshot, preserving selector
// roles and distinguishing an external candidate from a trusted current daemon.
// Monitoring discovery stays scoped to the attributed tree; operation/reporting
// observation also exposes what will block its next start.
func (d Discoverer) addDeletedCandidates(procs []Process, selectors []Selector) []Process {
	idx := snapshotIndexFor(d.reader())
	seen := make(map[int]bool, len(procs))
	for _, p := range procs {
		seen[p.PID] = true
	}
	for _, pid := range idx.deleted {
		if seen[pid] {
			continue
		}
		id := idx.byPID[pid]
		if d.foreignProcess(id) {
			continue
		}
		for i := range selectors {
			sel := &selectors[i]
			if sel.Delegated || !sel.HasStrictIdentity() || !d.matchesDeletedExe(sel, id, d.resolveUser()) {
				continue
			}
			p := toProcess(id, sel.Name, SelectorCommandMatch)
			p.External = true
			p.SignalBlockReason = d.deletedCandidateBlock(id, selectors, idx)
			procs = append(procs, p)
			break
		}
	}
	if slices.ContainsFunc(selectors, func(s Selector) bool { return s.Delegated }) {
		found := make(map[int]Process, len(procs))
		for _, p := range procs {
			found[p.PID] = p
		}
		d.markDelegated(selectors, found, idx, d.resolveUser())
		for i := range procs {
			procs[i] = found[procs[i].PID]
		}
	}
	return procs
}

func (d Discoverer) deletedCandidateBlock(id Identity, selectors []Selector, idx *snapshotIndex) string {
	if d.ProcessOwnership == nil {
		return "cannot verify service ownership of external process"
	}
	if _, known := d.ProcessOwnership(id); !known {
		return "cannot verify service ownership of external process"
	}
	// A shared helper cannot identify its instance. Count principal roots,
	// including foreign units, without treating the principal's workers as
	// additional instances. A cmd constraint still narrows the main selector.
	for i := range selectors {
		sel := &selectors[i]
		if sel.Name != RoleMain || !sel.HasStrictIdentity() || sel.Delegated {
			continue
		}
		roots := 0
		for _, other := range idx.byPID {
			if !d.claimedBy([]Selector{*sel}, other, d.resolveUser()) {
				continue
			}
			if d.claimedBy([]Selector{*sel}, idx.byPID[other.PPID], d.resolveUser()) {
				continue
			}
			roots++
			if d.foreignProcess(other) || roots > 1 {
				return "ambiguous service ownership of external process"
			}
		}
		return ""
	}
	return "external process has no strict main selector to distinguish instances"
}
