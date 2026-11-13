package process

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func supervisedFixture() (SupervisedProcessSpec, supervisedSource, map[int]Identity) {
	spec := SupervisedProcessSpec{Cgroup: "/openrc.worker", Exe: "/usr/bin/worker", User: "worker", SupervisorExe: "/usr/bin/supervisor", SupervisorUser: "root", RespawnGrace: time.Second}
	ids := map[int]Identity{
		21: {PID: 21, Exe: spec.Exe, UID: 7, ExeOK: true, StartTicks: 10, StartTicksOK: true, Cgroup: "0::" + spec.Cgroup},
		22: {PID: 22, Exe: spec.SupervisorExe, UID: 0, ExeOK: true, StartTicks: 11, StartTicksOK: true, Cgroup: "0::" + spec.Cgroup},
	}
	source := supervisedSource{reader: fakeReader{ids: ids}, interval: time.Millisecond,
		resolve: func(name string) (uint32, bool) {
			if name == "worker" {
				return 7, true
			}
			return 0, name == "root"
		},
		readFile: func(string) ([]byte, error) {
			var pids []string
			for _, pid := range slices.Sorted(maps.Keys(ids)) {
				pids = append(pids, strconv.Itoa(pid))
			}
			return []byte(strings.Join(pids, "\n")), nil
		},
	}
	return spec, source, ids
}

func TestSupervisedProcessObservation(t *testing.T) {
	for _, tt := range []struct {
		name       string
		change     func(map[int]Identity)
		wantWorker bool
		wantErr    bool
	}{
		{name: "healthy", wantWorker: true},
		{name: "respawning", change: func(ids map[int]Identity) { delete(ids, 21) }},
		{name: "no supervisor", change: func(ids map[int]Identity) { delete(ids, 22) }, wantErr: true},
		{name: "two workers", change: func(ids map[int]Identity) { id := ids[21]; id.PID = 23; ids[23] = id }, wantErr: true},
		{name: "two supervisors", change: func(ids map[int]Identity) { id := ids[22]; id.PID = 23; ids[23] = id }, wantErr: true},
		{name: "foreign cgroup", change: func(ids map[int]Identity) { id := ids[21]; id.Cgroup = "0::/other"; ids[21] = id }, wantErr: true},
		{name: "missing cgroup", change: func(ids map[int]Identity) { id := ids[21]; id.Cgroup = ""; ids[21] = id }, wantErr: true},
		{name: "wrong UID", change: func(ids map[int]Identity) { id := ids[21]; id.UID = 8; ids[21] = id }},
		{name: "unrelated executable", change: func(ids map[int]Identity) { id := ids[21]; id.Exe = "/other"; ids[21] = id }},
		{name: "zombie", change: func(ids map[int]Identity) { id := ids[21]; id.State = ProcStateZombie; ids[21] = id }},
		{name: "unreadable exe", change: func(ids map[int]Identity) { id := ids[21]; id.ExeOK = false; ids[21] = id }, wantErr: true},
		{name: "unknown generation", change: func(ids map[int]Identity) { id := ids[21]; id.StartTicksOK = false; ids[21] = id }, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spec, source, ids := supervisedFixture()
			if tt.change != nil {
				tt.change(ids)
			}
			result, err := source.observe(t.Context(), spec, 7, 0)
			if (result.worker != 0) != tt.wantWorker || (err != nil) != tt.wantErr {
				t.Fatalf("got %+v, %v", result, err)
			}
		})
	}
}

func TestSupervisedProcessRespawn(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(strconv.FormatBool(changed), func(t *testing.T) {
			spec, source, ids := supervisedFixture()
			worker := ids[21]
			delete(ids, 21)
			read, calls := source.readFile, 0
			source.readFile = func(path string) ([]byte, error) {
				calls++
				if calls == 3 {
					ids[21] = worker
					if changed {
						id := ids[22]
						id.StartTicks++
						ids[22] = id
					}
				}
				return read(path)
			}
			sample, err := source.wait(t.Context(), spec)
			if changed {
				if err == nil {
					t.Fatal("accepted a replacement supervisor")
				}
				return
			}
			if err != nil || sample.WorkerPID != 21 || sample.SupervisorPID != 22 || sample.Waited <= 0 {
				t.Fatalf("got %+v, %v", sample, err)
			}
		})
	}
}

func TestSupervisedProcessTimeoutAndCancellation(t *testing.T) {
	spec, source, ids := supervisedFixture()
	delete(ids, 21)
	spec.RespawnGrace = 5 * time.Millisecond
	if _, err := source.wait(t.Context(), spec); !errors.Is(err, ErrSupervisedNotReady) {
		t.Fatalf("missing worker: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := source.wait(ctx, spec); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
	source.resolve = func(string) (uint32, bool) { return 0, false }
	if _, err := source.wait(t.Context(), spec); err == nil {
		t.Fatal("accepted unresolved user")
	}
}

func TestSupervisedProcessMembershipBounds(t *testing.T) {
	for _, contents := range []string{"abc", "0", "1", "21\n21", strings.Repeat("21\n", 65)} {
		_, source, _ := supervisedFixture()
		source.readFile = func(string) ([]byte, error) { return []byte(contents), nil }
		if _, err := source.members("/test"); err == nil {
			t.Fatalf("accepted %q", contents)
		}
	}
	_, source, _ := supervisedFixture()
	for _, path := range []string{"/", "relative", "/a/../b"} {
		if _, err := source.members(path); err == nil {
			t.Fatalf("accepted group %q", path)
		}
	}
	source.readFile = func(string) ([]byte, error) { return nil, errors.New("denied") }
	if _, err := source.members("/test"); err == nil {
		t.Fatal("ignored read failure")
	}
}

type unstableSupervisedReader struct {
	fakeReader
	calls int
}

func (r *unstableSupervisedReader) Identity(pid int) (Identity, bool) {
	r.calls++
	id, ok := r.fakeReader.Identity(pid)
	if r.calls%2 == 0 {
		id.StartTicks++
	}
	return id, ok
}

func TestSupervisedProcessRejectsReusedPID(t *testing.T) {
	spec, source, ids := supervisedFixture()
	source.reader = &unstableSupervisedReader{ids: ids}
	if _, err := source.observe(t.Context(), spec, 7, 0); !errors.Is(err, errSupervisedUnstable) {
		t.Fatalf("reused PID: %v", err)
	}
}

func TestSupervisedProcessRetriesChangingMembership(t *testing.T) {
	spec, source, _ := supervisedFixture()
	read := source.readFile
	calls := 0
	source.readFile = func(path string) ([]byte, error) {
		calls++
		if calls == 2 {
			return []byte("22"), nil
		}
		return read(path)
	}
	sample, err := source.wait(t.Context(), spec)
	if err != nil || sample.WorkerPID != 21 || calls < 4 {
		t.Fatalf("got %+v, %v; reads=%d", sample, err, calls)
	}
}

type deniedSupervisedReader struct{ fakeReader }

func (r deniedSupervisedReader) IdentityWithError(pid int) (Identity, bool, error) {
	return Identity{}, false, errors.New("permission denied")
}

func TestSupervisedProcessReadErrorIsNotAbsence(t *testing.T) {
	spec, source, ids := supervisedFixture()
	source.reader = deniedSupervisedReader{ids: ids}
	if _, err := source.wait(t.Context(), spec); err == nil || errors.Is(err, ErrSupervisedNotReady) {
		t.Fatalf("read error treated as absent worker: %v", err)
	}
}
