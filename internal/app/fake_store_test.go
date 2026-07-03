package app

import (
	"fmt"
	"time"

	"sermo/internal/state"
)

type fakeStore struct {
	active     map[string]bool
	source     map[string]string
	updated    map[string]time.Time
	failSet    bool
	now        func() time.Time
	panicOn    bool
	panicFound bool
	panicSrc   string
	settling   map[string]state.OperationSettlingRecord
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		active:   map[string]bool{},
		source:   map[string]string{},
		updated:  map[string]time.Time{},
		now:      time.Now,
		settling: map[string]state.OperationSettlingRecord{},
	}
}

func (f *fakeStore) Active(service string) (active, found bool, err error) {
	if f == nil {
		return true, false, nil
	}
	active, found = f.active[service]
	return active, found, nil
}

func (f *fakeStore) SetActive(service string, active bool, source string) error {
	if f == nil {
		return nil
	}
	if f.failSet {
		return fmt.Errorf("set active failed")
	}
	f.active[service] = active
	f.source[service] = source
	f.updated[service] = f.now()
	return nil
}

func (f *fakeStore) Panic() (state.GlobalRecord, bool, error) {
	if f == nil {
		return state.GlobalRecord{}, false, nil
	}
	return state.GlobalRecord{On: f.panicOn, Source: f.panicSrc, UpdatedAt: f.now()}, f.panicFound, nil
}

func (f *fakeStore) SetPanic(on bool, source string) error {
	if f == nil {
		return nil
	}
	if f.failSet {
		return fmt.Errorf("set panic failed")
	}
	f.panicOn = on
	f.panicFound = true
	f.panicSrc = source
	return nil
}

func (f *fakeStore) MonitorState(service string) (state.MonitorRecord, bool, error) {
	if f == nil {
		return state.MonitorRecord{}, false, nil
	}
	active, found := f.active[service]
	if !found {
		return state.MonitorRecord{}, false, nil
	}
	return state.MonitorRecord{
		Active:    active,
		Source:    f.source[service],
		UpdatedAt: f.updated[service],
	}, true, nil
}

func (f *fakeStore) SetOperationSettling(service, action, phase, source string) error {
	if f == nil {
		return nil
	}
	if f.failSet {
		return fmt.Errorf("set operation settling failed")
	}
	f.settling[service] = state.OperationSettlingRecord{
		Action:    action,
		Phase:     phase,
		Source:    source,
		UpdatedAt: f.now(),
	}
	return nil
}

func (f *fakeStore) OperationSettling(service string) (state.OperationSettlingRecord, bool, error) {
	if f == nil {
		return state.OperationSettlingRecord{}, false, nil
	}
	rec, found := f.settling[service]
	return rec, found, nil
}

func (f *fakeStore) ClearOperationSettling(service string) error {
	if f == nil {
		return nil
	}
	if f.failSet {
		return fmt.Errorf("clear operation settling failed")
	}
	delete(f.settling, service)
	return nil
}
