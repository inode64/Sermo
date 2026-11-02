package checks

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"testing"
	"time"

	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
	"sermo/internal/process"
)

type terminalResidualReader map[int]process.Identity

func (r terminalResidualReader) PIDs() ([]int, error) {
	pids := make([]int, 0, len(r))
	for pid := range r {
		pids = append(pids, pid)
	}
	return pids, nil
}

func (r terminalResidualReader) Identity(pid int) (process.Identity, bool) {
	id, ok := r[pid]
	return id, ok
}

func residualScreenFixture() (TerminalSessionConfig, terminalResidualReader) {
	config := TerminalSessionConfig{Multiplexer: TerminalMultiplexerScreen, Binary: "/usr/bin/screen-4.9.1", User: "1000"}
	reader := terminalResidualReader{120: {
		PID: 120, PPID: 1, UID: 1000, TTYOK: true, State: "S", StartTicks: 1200, StartTicksOK: true,
		ExePrev: config.Binary, ExeFile: process.ExecutableFile{Device: 1, Inode: 99}, Cgroup: "0::/login",
	}}
	config.ResidualProcesses = NewTerminalResidualSampler(reader, testSSHLookup())
	return config, reader
}

func TestResidualScreenRequiresExactDeletedServerIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*process.Identity)
		want   bool
	}{
		{name: "verified server", want: true},
		{name: "different path", change: func(id *process.Identity) { id.ExePrev = "/other/screen-4.9.1" }},
		{name: "unreadable exe", change: func(id *process.Identity) { id.ExePrev = "" }},
		{name: "unpinned file", change: func(id *process.Identity) { id.ExeFile.Inode = 0 }},
		{name: "different user", change: func(id *process.Identity) { id.UID++ }},
		{name: "current executable", change: func(id *process.Identity) { id.ExeOK = true }},
		{name: "client terminal", change: func(id *process.Identity) { id.TTY = 34816 }},
		{name: "unknown terminal", change: func(id *process.Identity) { id.TTYOK = false }},
		{name: "not reparented", change: func(id *process.Identity) { id.PPID = 110 }},
		{name: "zombie", change: func(id *process.Identity) { id.State = process.ProcStateZombie }},
		{name: "unknown generation", change: func(id *process.Identity) { id.StartTicksOK = false }},
		{name: "zero generation", change: func(id *process.Identity) { id.StartTicks = 0 }},
		{name: "init", change: func(id *process.Identity) { id.PID = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, reader := residualScreenFixture()
			id := reader[120]
			if tc.change != nil {
				tc.change(&id)
				reader[120] = id
			}
			procs, err := config.residualProcesses(t.Context())
			if err != nil || (len(procs) == 1) != tc.want {
				t.Fatalf("processes=%+v err=%v, want match=%v", procs, err, tc.want)
			}
		})
	}
}

func TestTerminalSessionsRecoversDeletedScreenOnlyAfterMissingClient(t *testing.T) {
	for _, tc := range []struct {
		name         string
		err          error
		exit         int
		absent, tmux bool
		want         bool
	}{
		{name: "missing client with live server", err: fs.ErrNotExist, exit: execx.ExitCodeRunFailure, want: true},
		{name: "missing client and server", err: fs.ErrNotExist, exit: execx.ExitCodeRunFailure, absent: true},
		{name: "permission denied", err: fs.ErrPermission, exit: execx.ExitCodeRunFailure},
		{name: "client started but failed", err: fs.ErrNotExist, exit: 1},
		{name: "tmux namespace needs its client", err: fs.ErrNotExist, exit: execx.ExitCodeRunFailure, tmux: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, reader := residualScreenFixture()
			if tc.absent {
				delete(reader, 120)
			}
			if tc.tmux {
				config.Multiplexer = TerminalMultiplexerTmux
			}
			runner := execxtest.Fixed(execx.Result{ExitCode: tc.exit}, tc.err)
			check := terminalSessionsCheck{
				name: "sessions", timeout: time.Second, config: config, runner: runner,
				preds: []levelPred{{field: DataKeyCount, op: ">", value: 0}},
			}
			result := check.Run(t.Context())
			if result.Unavailable == tc.want || len(runner.Calls()) != 1 {
				t.Fatalf("result=%+v calls=%+v", result, runner.Calls())
			}
			if tc.want {
				sessions := TerminalSessionsFromData(result.Data)
				if len(sessions) != 1 || sessions[0].State != TerminalSessionStateResidual || sessions[0].Identity == "" || !slices.Equal(sessions[0].PIDs, []int{120}) || result.Data[DataKeyCount] != 1 {
					t.Fatalf("residual sessions=%+v data=%+v", sessions, result.Data)
				}
			}
		})
	}
}

func TestResidualScreenCloseRevalidatesDisplayedGeneration(t *testing.T) {
	for _, name := range []string{"unchanged", "pid reused", "file replaced", "user changed", "gone", "wrong name", "wrong user", "wrong multiplexer", "bad config", "cancelled", "snapshot failed", "unknown user"} {
		t.Run(name, func(t *testing.T) {
			config, reader := residualScreenFixture()
			procs, err := config.residualProcesses(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			want := residualScreenSession(config, procs[0])
			id := reader[120]
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch name {
			case "pid reused":
				id.StartTicks++
			case "file replaced":
				id.ExeFile.Inode++
			case "user changed":
				id.UID++
			case "wrong name":
				want.Name = "another"
			case "wrong user":
				want.User = "0"
			case "wrong multiplexer":
				config.Multiplexer, want.Multiplexer = TerminalMultiplexerTmux, TerminalMultiplexerTmux
			case "bad config":
				config.Binary = "relative"
			case "cancelled":
				cancel()
			case "snapshot failed":
				config.ResidualProcesses = NewTerminalResidualSampler(failingSSHSnapshotReader{}, testSSHLookup())
			case "unknown user":
				config.User, want.User = "unknown", "unknown"
			}
			reader[120] = id
			if name == "gone" {
				delete(reader, 120)
			}
			got, err := VerifyResidualTerminalSession(ctx, config, want)
			if (err == nil) != (name == "unchanged") {
				t.Fatalf("target=%+v err=%v", got, err)
			}
			if name == "unchanged" && (got.ExePrev != config.Binary || got.ExeFile != id.ExeFile || got.UID != id.UID || got.Cgroup != id.Cgroup) {
				t.Fatalf("lost signal evidence: %+v", got)
			}
			if name == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
