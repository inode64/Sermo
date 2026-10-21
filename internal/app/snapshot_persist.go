package app

import (
	"maps"
	"reflect"
	"slices"
	"sync"
	"time"

	"sermo/internal/state"
)

// snapshotPersistence keeps a snapshot registry's store rows in step with its
// published readings while writing only what changed. A cycle that re-ran a
// check and got the same reading leaves the stored row, and its older time,
// alone; Flush writes those times when sermod stops cleanly, so the next start
// hydrates current readings. After a crash the unchanged rows load with the
// time they were last written, and freshness marks them stale until their
// first cycle.
type snapshotPersistence struct {
	// writeMu serializes store writes so an older delta never lands after a
	// newer one; mu guards stored and is never held across a write.
	writeMu sync.Mutex
	mu      sync.Mutex
	stored  map[string]map[string]storedSnapshot
	// dropMissing deletes a stored row its group no longer publishes: a service
	// publishes its whole check set every cycle, a watch one slot at a time.
	dropMissing bool
	write       func(map[string]state.CheckSnapshotDelta) error
}

func newSnapshotPersistence(records map[string]map[string]state.CheckSnapshotRecord, dropMissing bool, write func(map[string]state.CheckSnapshotDelta) error) *snapshotPersistence {
	p := &snapshotPersistence{stored: make(map[string]map[string]storedSnapshot, len(records)), dropMissing: dropMissing, write: write}
	for group, rows := range records {
		stored := make(map[string]storedSnapshot, len(rows))
		for name, rec := range rows {
			stored[name] = newStoredSnapshot(snapshotFromRecord(rec))
		}
		p.stored[group] = stored
	}
	return p
}

// changed reports whether published holds a reading group's stored rows do not
// (or, with dropMissing, lacks one they hold).
func (p *snapshotPersistence) changed(group string, published map[string]CheckSnapshot) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	stored := p.stored[group]
	for name, snap := range published {
		row, ok := stored[name]
		if !ok || !row.sameReading(newStoredSnapshot(snap), false) {
			return true
		}
	}
	if p.dropMissing {
		for name := range stored {
			if _, ok := published[name]; !ok {
				return true
			}
		}
	}
	return false
}

// persist writes, in one store call, the rows of groups whose reading differs
// from the stored row, or whose time does too when withTime is set. current
// returns a group's published snapshots; it is read once writeMu is held, so
// the store ends with the latest of concurrent publishes. A failed write leaves
// stored as it was, and the next publish of that group retries.
func (p *snapshotPersistence) persist(groups []string, current func(string) map[string]CheckSnapshot, withTime bool) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	published := make(map[string]map[string]CheckSnapshot, len(groups))
	for _, group := range groups {
		published[group] = current(group)
	}
	deltas := map[string]state.CheckSnapshotDelta{}
	written := map[string]map[string]storedSnapshot{}
	p.mu.Lock()
	for group, snaps := range published {
		stored := p.stored[group]
		var delta state.CheckSnapshotDelta
		rows := map[string]storedSnapshot{}
		for name, snap := range snaps {
			row := newStoredSnapshot(snap)
			if prev, ok := stored[name]; ok && prev.sameReading(row, withTime) {
				continue
			}
			if delta.Upsert == nil {
				delta.Upsert = map[string]state.CheckSnapshotRecord{}
			}
			delta.Upsert[name] = snapshotRecord(snap)
			rows[name] = row
		}
		if p.dropMissing {
			for name := range stored {
				if _, ok := snaps[name]; !ok {
					delta.Delete = append(delta.Delete, name)
				}
			}
		}
		if len(delta.Upsert) > 0 || len(delta.Delete) > 0 {
			deltas[group], written[group] = delta, rows
		}
	}
	p.mu.Unlock()
	if len(deltas) == 0 {
		return nil
	}
	if err := p.write(deltas); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for group, delta := range deltas {
		stored := p.stored[group]
		if stored == nil {
			stored = map[string]storedSnapshot{}
			p.stored[group] = stored
		}
		maps.Copy(stored, written[group])
		for _, name := range delta.Delete {
			delete(stored, name)
		}
	}
	return nil
}

// flush writes every published reading of published whose time is newer than
// its stored row; mu is the registry lock guarding published. A nil
// persistence (no store) writes nothing.
func (p *snapshotPersistence) flush(mu *sync.RWMutex, published map[string]map[string]CheckSnapshot, current func(string) map[string]CheckSnapshot) error {
	if p == nil {
		return nil
	}
	mu.RLock()
	groups := slices.Sorted(maps.Keys(published))
	mu.RUnlock()
	return p.persist(groups, current, true)
}

// storedSnapshot is the row the store holds for one reading: the snapshot
// without its data map, which is kept in the store's encoding so a reading
// decoded from the store and one fresh from a check compare equal.
type storedSnapshot struct {
	snap CheckSnapshot
	data string
}

func newStoredSnapshot(snap CheckSnapshot) storedSnapshot {
	data, err := state.EncodeCheckSnapshotData(snap.Data)
	if err != nil {
		// Never equal to a stored row, so the write is attempted and the store
		// reports the encoding error.
		data = ""
	}
	snap.Data = nil
	return storedSnapshot{snap: snap, data: data}
}

// sameReading reports whether two rows hold the same reading; withTime also
// requires the same time.
func (a storedSnapshot) sameReading(b storedSnapshot, withTime bool) bool {
	if a.data != b.data || withTime && !a.snap.At.Equal(b.snap.At) {
		return false
	}
	a.snap.At, b.snap.At = time.Time{}, time.Time{}
	return reflect.DeepEqual(a.snap, b.snap)
}
