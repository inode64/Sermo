package process

import (
	"errors"
	"testing"
)

func TestObserveDistinguishesAbsenceFromUncertainty(t *testing.T) {
	t.Parallel()
	main := Selector{Name: RoleMain, Type: SelectorCommandMatch, Exe: "/opt/apache", User: "root"}
	worker := Selector{Name: "worker", Type: SelectorCommandMatch, Exe: "/opt/apache", User: "apache"}
	tests := []struct {
		name           string
		ids            map[int]Identity
		selectors      []Selector
		backend        []int
		trusted, known bool
		replaced       bool
		count          int
	}{
		{name: "confirmed absence", selectors: []Selector{main}, known: true},
		{name: "no selectors", known: false},
		{name: "unresolved user", selectors: []Selector{{Type: SelectorCommandMatch, Exe: "/opt/apache", User: "unknown"}}},
		{name: "command alone cannot prove absence", selectors: []Selector{{Type: SelectorCommandMatch, Cmd: "apache"}}},
		{name: "live main", selectors: []Selector{main}, ids: map[int]Identity{100: {PID: 100, UID: 0, Exe: "/opt/apache", ExeOK: true}}, trusted: true, known: true, count: 1},
		{name: "unreadable attributed executable", selectors: []Selector{main}, backend: []int{100}, ids: map[int]Identity{100: {PID: 100, UID: 0, PPID: 1}}, count: 1},
		{name: "unreadable unseeded executable", selectors: []Selector{main}, ids: map[int]Identity{100: {PID: 100, UID: 0, PPID: 1}}},
		{name: "deleted unseeded executable survives", selectors: []Selector{main}, ids: map[int]Identity{100: {PID: 100, UID: 0, PPID: 1, ExePrev: "/opt/apache"}}, replaced: true, known: true, count: 1},
		{name: "deleted worker is not main evidence", selectors: []Selector{main, worker}, backend: []int{100}, ids: map[int]Identity{100: {PID: 100, UID: 33, PPID: 1, ExePrev: "/opt/apache"}}, known: true, count: 1},
		{name: "unrelated attributed deleted exe", selectors: []Selector{main}, backend: []int{100}, ids: map[int]Identity{100: {PID: 100, UID: 0, PPID: 1, ExePrev: "/opt/unrelated"}}, known: true, count: 1},
		{name: "deleted zombie is absent", selectors: []Selector{main}, ids: map[int]Identity{100: {PID: 100, UID: 0, PPID: 1, State: ProcStateZombie, ExePrev: "/opt/apache"}}, known: true},
		{name: "zombie backend seed cannot hide live main", selectors: []Selector{main}, backend: []int{100}, ids: map[int]Identity{
			100: {PID: 100, UID: 0, PPID: 1, State: ProcStateZombie},
			101: {PID: 101, UID: 0, PPID: 1, Exe: "/opt/apache", ExeOK: true},
		}, trusted: true, known: true, count: 1},
		{name: "worker is not the main", selectors: []Selector{main, worker}, ids: map[int]Identity{100: {PID: 100, UID: 33, Exe: "/opt/apache", ExeOK: true}}, known: true, count: 1},
		{name: "kernel thread is not uncertainty", selectors: []Selector{main}, ids: map[int]Identity{100: {PID: 100, UID: 0, PPID: 2}}, known: true},
		{name: "unrelated replaced binary", selectors: []Selector{main}, ids: map[int]Identity{100: {PID: 100, UID: 0, PPID: 1, ExePrev: "/opt/unrelated"}}, known: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := Discoverer{Reader: fakeReader{ids: tt.ids}, ResolveUser: fakeUsers(map[string]uint32{"root": 0, "apache": 33}), BackendPIDs: func() []int { return tt.backend }}
			out, err := d.Observe(tt.selectors)
			if err != nil || out.Trusted != tt.trusted || out.ReplacedExecutable != tt.replaced || out.AbsenceKnown != tt.known || len(out.Processes) != tt.count {
				t.Fatalf("observation=%+v err=%v; want trusted=%v known=%v count=%d", out, err, tt.trusted, tt.known, tt.count)
			}
		})
	}
}

func TestObserveRejectsIncompleteSnapshot(t *testing.T) {
	t.Parallel()
	d := Discoverer{Reader: &errorAwareSnapshotReader{err: errors.New("permission denied")}}
	if _, err := d.Observe([]Selector{{Type: SelectorCommandMatch, Exe: "/opt/apache", User: "root"}}); err == nil {
		t.Fatal("an unreadable process table must not prove absence")
	}
}

func TestObserveTrackedWithoutSelectorsOrBackendPIDs(t *testing.T) {
	t.Parallel()
	old := []Process{{PID: 100, StartTicks: 10, Source: SourceBackend}}
	for _, tc := range []struct {
		name     string
		ids      map[int]Identity
		wantLive bool
	}{
		{name: "exited", ids: map[int]Identity{}},
		{name: "escaped attribution", ids: map[int]Identity{100: {PID: 100, StartTicks: 10, StartTicksOK: true}}, wantLive: true},
		{name: "PID reused", ids: map[int]Identity{100: {PID: 100, StartTicks: 20, StartTicksOK: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := Discoverer{Reader: fakeReader{ids: tc.ids}}
			out, err := d.ObserveTracked(nil, old)
			if err != nil || (len(out.Processes) > 0) != tc.wantLive || out.AbsenceKnown {
				t.Fatalf("tracked=%+v error=%v", out, err)
			}
			if err := out.VerifyExited(old); (err != nil) != tc.wantLive {
				t.Fatalf("exit proof=%v, survivor=%v", err, tc.wantLive)
			}
		})
	}
	d := Discoverer{Reader: &errorAwareSnapshotReader{err: errors.New("unreadable snapshot")}}
	if _, err := d.ObserveTracked(nil, old); err == nil {
		t.Fatal("tracked exit must reject an incomplete snapshot")
	}
	if _, err := d.ObserveTracked(nil, nil); err != nil {
		t.Fatal("unconfigured monitoring must keep its no-scan fast path")
	}
}

func TestObserveUsesOneFreshSnapshot(t *testing.T) {
	t.Parallel()
	inner := &countingReader{ids: map[int]Identity{100: {PID: 100, UID: 0, Exe: "/opt/apache", ExeOK: true}}}
	d := Discoverer{Reader: NewCachingReader(inner, 0), ResolveUser: fakeUsers(map[string]uint32{"root": 0})}
	selectors := []Selector{{Type: SelectorCommandMatch, Exe: "/opt/apache", User: "root"}}
	for range 2 {
		if _, err := d.Observe(selectors); err != nil {
			t.Fatal(err)
		}
	}
	if inner.pidCalls != 2 {
		t.Fatalf("snapshot walks=%d, want one per observation", inner.pidCalls)
	}
}

func TestObserveKeepsZombiesInSharedMonitoringSnapshot(t *testing.T) {
	t.Parallel()
	reader := &errorAwareSnapshotReader{snapshot: map[int]Identity{
		100: {PID: 100, UID: 0, PPID: 1, State: ProcStateZombie},
		101: {PID: 101, UID: 0, PPID: 1, Exe: "/opt/apache", ExeOK: true},
	}}
	d := Discoverer{Reader: reader, ResolveUser: fakeUsers(map[string]uint32{"root": 0}), BackendPIDs: func() []int { return []int{100} }}
	out, err := d.Observe([]Selector{{Type: SelectorCommandMatch, Exe: "/opt/apache", User: "root"}})
	if err != nil || !out.Trusted || len(out.Processes) != 1 || out.Processes[0].PID != 101 {
		t.Fatalf("observation=%+v err=%v", out, err)
	}
	if len(reader.snapshot) != 2 || reader.snapshot[100].State != ProcStateZombie {
		t.Fatalf("operation mutated the monitoring snapshot: %+v", reader.snapshot)
	}
}

func TestObservationVerifiesExitedGenerations(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		ids      map[int]Identity
		oldTicks uint64
		wantErr  bool
	}{
		{name: "exited", oldTicks: 10, ids: map[int]Identity{}},
		{name: "missing snapshot", oldTicks: 10, wantErr: true},
		{name: "missing old generation", ids: map[int]Identity{}, wantErr: true},
		{name: "unchanged outside unit", oldTicks: 10, ids: map[int]Identity{100: {PID: 100, StartTicks: 10, StartTicksOK: true}}, wantErr: true},
		{name: "reused PID", oldTicks: 10, ids: map[int]Identity{100: {PID: 100, StartTicks: 20, StartTicksOK: true}}},
		{name: "unknown current generation", oldTicks: 10, ids: map[int]Identity{100: {PID: 100}}, wantErr: true},
		{name: "zombie exited", oldTicks: 10, ids: map[int]Identity{100: {PID: 100, State: ProcStateZombie, StartTicks: 10, StartTicksOK: true}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out := Observation{snapshot: withoutZombieIdentities(tt.ids)}
			err := out.VerifyExited([]Process{{PID: 100, StartTicks: tt.oldTicks}})
			if (err != nil) != tt.wantErr {
				t.Fatalf("VerifyExited()=%v, want error=%v", err, tt.wantErr)
			}
		})
	}
}
