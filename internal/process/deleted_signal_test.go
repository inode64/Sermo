package process

import (
	"errors"
	"slices"
	"syscall"
	"testing"
)

func deletedSignalTarget() Process {
	return Process{PID: 100, StartTicks: 42, UID: 110, ExePrev: testExe,
		ExeFile: ExecutableFile{Device: 1, Inode: 20}}
}

func TestPIDFDDeletedFileStaysPinnedThroughDelivery(t *testing.T) {
	t.Parallel()
	for _, failPin := range []bool{false, true} {
		t.Run(map[bool]string{false: "verified", true: "unreadable"}[failPin], func(t *testing.T) {
			t.Parallel()
			p := deletedSignalTarget()
			var steps []string
			sender := pidfdSignaler{
				open:  func(int, int) (int, error) { steps = append(steps, "pidfd"); return 7, nil },
				close: func(int) error { steps = append(steps, "close pidfd"); return nil },
				pinDeleted: func(Process) (func(), error) {
					steps = append(steps, "pin executable")
					if failPin {
						return nil, errors.New("unreadable executable fixture")
					}
					return func() { steps = append(steps, "release executable") }, nil
				},
				identity: func(int) (Identity, bool) {
					steps = append(steps, "identity")
					return Identity{PID: p.PID, StartTicks: p.StartTicks, StartTicksOK: true, UID: p.UID, ExePrev: p.ExePrev, ExeFile: p.ExeFile}, true
				},
				send: func(int, syscall.Signal) error { steps = append(steps, "signal"); return nil },
			}
			err := sender.signal(t.Context(), p, syscall.SIGTERM)
			want := []string{"pidfd", "pin executable", "identity", "signal", "release executable", "close pidfd"}
			if failPin {
				want = []string{"pidfd", "pin executable", "close pidfd"}
			}
			if (err != nil) != failPin || !slices.Equal(steps, want) {
				t.Fatalf("err=%v steps=%v, want %v", err, steps, want)
			}
		})
	}
}

func TestReapDoesNotKillReplacementGeneration(t *testing.T) {
	t.Parallel()
	p := deletedSignalTarget()
	replacement := p
	replacement.StartTicks++
	replacement.ExeFile.Inode++
	signaler := &recSignaler{}
	reaper := newReaper(signaler, [][]Process{{replacement}})
	result := reaper.Reap(t.Context(), []Process{p}, killPolicy)
	if !slices.Equal(signaler.sigsFor(p.PID), []syscall.Signal{syscall.SIGTERM}) {
		t.Fatalf("replacement received signal: %v", signaler.calls)
	}
	if result.OK() || len(result.Remaining) != 1 || result.Remaining[0].SignalBlockReason == "" {
		t.Fatalf("replacement must remain a diagnosed orphan: %+v", result)
	}
}

func TestDeletedExecutableCleanupAuthorization(t *testing.T) {
	t.Parallel()
	resolve := fakeUsers(map[string]uint32{"mysql": 110})
	selector := NewKillSelector([]string{"mysql"}, []string{testExe})
	for _, tc := range []struct {
		name   string
		change func(*Process)
		want   bool
	}{
		{name: "verified deleted executable", want: true},
		{name: "path alone", change: func(p *Process) { p.ExeFile = ExecutableFile{} }},
		{name: "different path", change: func(p *Process) { p.ExePrev = "/other" }},
		{name: "different user", change: func(p *Process) { p.UID++ }},
		{name: "ambiguous owner", change: func(p *Process) { p.SignalBlockReason = "ambiguous service ownership" }},
		{name: "delegated", change: func(p *Process) { p.Delegated = true }},
		{name: "protected", change: func(p *Process) { p.PID = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := deletedSignalTarget()
			if tc.change != nil {
				tc.change(&p)
			}
			if got := selector.Killable(p, resolve); got != tc.want {
				t.Fatalf("Killable=%v, want %v: %+v", got, tc.want, p)
			}
		})
	}
}

func TestPIDFDDeletedExecutableVerification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*Identity)
		signal syscall.Signal
		want   bool
	}{
		{name: "term", signal: syscall.SIGTERM, want: true},
		{name: "kill", signal: syscall.SIGKILL, want: true},
		{name: "reload refused", signal: syscall.SIGHUP},
		{name: "recycled pid", change: func(id *Identity) { id.StartTicks++ }},
		{name: "exec same path different inode", change: func(id *Identity) { id.ExeFile.Inode++ }},
		{name: "different device", change: func(id *Identity) { id.ExeFile.Device++ }},
		{name: "different user", change: func(id *Identity) { id.UID++ }},
		{name: "changed cgroup", change: func(id *Identity) { id.Cgroup = "0::/other.service" }},
		{name: "different path", change: func(id *Identity) { id.ExePrev = "/other" }},
		{name: "file unreadable", change: func(id *Identity) { id.ExeFile = ExecutableFile{} }},
		{name: "became current executable", change: func(id *Identity) { id.ExePrev = ""; id.Exe = testExe; id.ExeOK = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := deletedSignalTarget()
			id := Identity{PID: p.PID, StartTicks: p.StartTicks, StartTicksOK: true, UID: p.UID, ExePrev: p.ExePrev, ExeFile: p.ExeFile}
			if tc.change != nil {
				tc.change(&id)
			}
			sent := false
			sender := pidfdSignaler{
				open:     func(int, int) (int, error) { return 7, nil },
				close:    func(int) error { return nil },
				identity: func(int) (Identity, bool) { return id, true },
				send:     func(int, syscall.Signal) error { sent = true; return nil },
			}
			sig := tc.signal
			if sig == 0 {
				sig = syscall.SIGTERM
			}
			err := sender.signal(t.Context(), p, sig)
			if sent != tc.want || (err == nil) != tc.want {
				t.Fatalf("sent=%v err=%v, want send=%v", sent, err, tc.want)
			}
		})
	}
}
