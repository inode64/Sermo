package pkgdb

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
)

const rpmQueryLine = "rpm -qf -- "

func rpmIndex(t *testing.T, runner execx.Runner, now func() time.Time) *Index {
	t.Helper()
	root := t.TempDir()
	mkdir(t, root, rpmDBDir)
	x := New(Options{Root: root, Runner: runner, Timeout: time.Second, Now: now})
	if err := x.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got := x.Backend(); got != BackendRPM {
		t.Fatalf("Backend = %q, want %q", got, BackendRPM)
	}
	return x
}

func TestRPMOwnedAllRunsOneCommandAndCaches(t *testing.T) {
	runner := &execxtest.Runner{
		ByLine: map[string]execx.Result{
			rpmQueryLine + "/usr/bin/bash /opt/rogue /usr/bin/python3": {
				Stdout: "bash-5.2.26-3.fc40.x86_64\nfile /opt/rogue is not owned by any package\npython3-3.12.4-1.fc40.x86_64\n",
				// rpm exits 1 when any argument is unowned.
				ExitCode: 1,
			},
		},
		Err: errors.New("unexpected command line"),
	}
	x := rpmIndex(t, runner, nil)
	got := x.OwnedAll(t.Context(), []string{"/usr/bin/bash", "/opt/rogue", "/usr/bin/python3"})
	want := map[string]Ownership{"/usr/bin/bash": Owned, "/opt/rogue": NotOwned, "/usr/bin/python3": Owned}
	for exe, own := range want {
		if got[exe] != own {
			t.Errorf("OwnedAll[%q] = %v, want %v", exe, got[exe], own)
		}
	}
	if n := len(runner.Calls()); n != 1 {
		t.Fatalf("rpm ran %d times, want 1: %v", n, runner.Lines())
	}
	if !runner.SawDeadline() {
		t.Fatal("rpm ran without a context deadline")
	}
	// Every answer is cached: no further command for the same paths, in any
	// order, through Owned or OwnedAll.
	expectOwnership(t, x, want)
	if got := x.Owned(t.Context(), "/usr/bin/../bin/bash"); got != Owned {
		t.Fatalf("Owned(unclean) = %v, want Owned", got)
	}
	if n := len(runner.Calls()); n != 1 {
		t.Fatalf("rpm ran %d times after caching, want 1: %v", n, runner.Lines())
	}
	// A new path runs exactly one more command, for that path alone.
	runner.ByLine[rpmQueryLine+"/usr/sbin/new"] = execx.Result{Stdout: "new-1.0-1.x86_64\n"}
	got = x.OwnedAll(t.Context(), []string{"/usr/bin/bash", "/usr/sbin/new", "/opt/rogue"})
	if got["/usr/sbin/new"] != Owned || got["/usr/bin/bash"] != Owned || got["/opt/rogue"] != NotOwned {
		t.Fatalf("OwnedAll mixed cached/new = %v", got)
	}
	if lines := runner.Lines(); len(lines) != 2 || lines[1] != rpmQueryLine+"/usr/sbin/new" {
		t.Fatalf("rpm command lines = %v", lines)
	}
}

func TestRPMCommandErrorIsUnknownAndNotCached(t *testing.T) {
	runner := execxtest.Fixed(execx.Result{ExitCode: execx.ExitCodeRunFailure}, errors.New("rpm: not found"))
	x := rpmIndex(t, runner, nil)
	for range 2 {
		got := x.OwnedAll(t.Context(), []string{"/usr/bin/bash", "/opt/rogue"})
		if got["/usr/bin/bash"] != Unknown || got["/opt/rogue"] != Unknown {
			t.Fatalf("OwnedAll after command error = %v, want all Unknown", got)
		}
	}
	if n := len(runner.Calls()); n != 2 {
		t.Fatalf("rpm ran %d times, want 2 (failures are not cached)", n)
	}
}

func TestRPMContextTimeoutIsUnknown(t *testing.T) {
	runner := &execxtest.Runner{RespectContext: true, Default: execx.Result{Stdout: "bash-5.2.26-3.fc40.x86_64\n"}}
	x := rpmIndex(t, runner, nil)
	ctx, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	if got := x.Owned(ctx, "/usr/bin/bash"); got != Unknown {
		t.Fatalf("Owned with expired context = %v, want Unknown", got)
	}
	// Nothing was cached: a live context runs the command and gets the answer.
	if got := x.Owned(t.Context(), "/usr/bin/bash"); got != Owned {
		t.Fatalf("Owned with live context = %v, want Owned", got)
	}
	if n := len(runner.Calls()); n != 2 {
		t.Fatalf("rpm ran %d times, want 2", n)
	}
}

func TestRPMNilRunnerIsUnknown(t *testing.T) {
	x := rpmIndex(t, nil, nil)
	expectOwnership(t, x, map[string]Ownership{"/usr/bin/bash": Unknown, "/opt/rogue": Unknown})
}

func TestRPMDatabaseErrorIsUnknown(t *testing.T) {
	runner := execxtest.Fixed(execx.Result{
		Stderr:   "error: cannot open Packages database in /var/lib/rpm\n",
		ExitCode: 1,
	}, nil)
	x := rpmIndex(t, runner, nil)
	for range 2 {
		if got := x.Owned(t.Context(), "/usr/bin/bash"); got != Unknown {
			t.Fatalf("Owned with broken database = %v, want Unknown", got)
		}
	}
	if n := len(runner.Calls()); n != 2 {
		t.Fatalf("rpm ran %d times, want 2 (undetermined answers are not cached)", n)
	}
}

func TestRPMMissingFileStaysUnknownOthersAnswered(t *testing.T) {
	runner := execxtest.Fixed(execx.Result{
		Stdout:   "bash-5.2.26-3.fc40.x86_64\n",
		Stderr:   "error: file /usr/bin/gone: No such file or directory\n",
		ExitCode: 1,
	}, nil)
	x := rpmIndex(t, runner, nil)
	got := x.OwnedAll(t.Context(), []string{"/usr/bin/bash", "/usr/bin/gone"})
	if got["/usr/bin/bash"] != Owned || got["/usr/bin/gone"] != Unknown {
		t.Fatalf("OwnedAll = %v, want bash Owned and gone Unknown", got)
	}
	runner.ByLine = map[string]execx.Result{rpmQueryLine + "/usr/bin/gone": {Stdout: "gone-1.0-1.x86_64\n"}}
	got = x.OwnedAll(t.Context(), []string{"/usr/bin/bash", "/usr/bin/gone"})
	if got["/usr/bin/bash"] != Owned || got["/usr/bin/gone"] != Owned {
		t.Fatalf("OwnedAll after reappearance = %v", got)
	}
	if lines := runner.Lines(); len(lines) != 2 || lines[1] != rpmQueryLine+"/usr/bin/gone" {
		t.Fatalf("rpm command lines = %v, want the missing path re-queried alone", lines)
	}
}

func TestRPMRelativePathIsUnknownWithoutCommand(t *testing.T) {
	runner := execxtest.Outputs("bash-5.2.26-3.fc40.x86_64\n")
	x := rpmIndex(t, runner, nil)
	if got := x.Owned(t.Context(), "usr/bin/bash"); got != Unknown {
		t.Fatalf("Owned(relative) = %v, want Unknown", got)
	}
	if runner.Ran(rpmCommand) {
		t.Fatal("rpm ran for a relative path")
	}
}

func TestRPMCacheClearsOnRefreshChangeAndTTL(t *testing.T) {
	clock := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	runner := execxtest.Outputs("bash-5.2.26-3.fc40.x86_64\n")
	x := rpmIndex(t, runner, now)
	for range 2 {
		if got := x.Owned(t.Context(), "/usr/bin/bash"); got != Owned {
			t.Fatalf("Owned = %v, want Owned", got)
		}
	}
	if n := len(runner.Calls()); n != 1 {
		t.Fatalf("rpm ran %d times, want 1", n)
	}
	// An unchanged database keeps the cache across Refresh.
	if err := x.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	x.Owned(t.Context(), "/usr/bin/bash")
	if n := len(runner.Calls()); n != 1 {
		t.Fatalf("rpm ran %d times after unchanged Refresh, want 1", n)
	}
	// A database change invalidates it.
	chtimes(t, filepath.Join(x.root, rpmDBDir), clock.Add(time.Minute))
	if err := x.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	x.Owned(t.Context(), "/usr/bin/bash")
	if n := len(runner.Calls()); n != 2 {
		t.Fatalf("rpm ran %d times after database change, want 2", n)
	}
	// So does the cache lifetime.
	clock = clock.Add(rpmCacheTTL + time.Second)
	x.Owned(t.Context(), "/usr/bin/bash")
	if n := len(runner.Calls()); n != 3 {
		t.Fatalf("rpm ran %d times after TTL, want 3", n)
	}
}

func TestParseRPMQuery(t *testing.T) {
	paths := []string{"/a", "/b", "/c"}
	tests := []struct {
		name   string
		output string
		want   map[string]Ownership
	}{
		{
			name:   "all owned",
			output: "pa-1\npb-1\npc-1\n",
			want:   map[string]Ownership{"/a": Owned, "/b": Owned, "/c": Owned},
		},
		{
			name:   "mixed",
			output: "pa-1\nfile /b is not owned by any package\npc-1\n",
			want:   map[string]Ownership{"/a": Owned, "/b": NotOwned, "/c": Owned},
		},
		{
			name:   "multiple owners of one file",
			output: "pa-1\npa-2\nfile /b is not owned by any package\npc-1\n",
			want:   map[string]Ownership{"/a": Owned, "/b": NotOwned, "/c": Owned},
		},
		{
			name:   "stderr error for one path",
			output: "pa-1\n\nerror: file /b: No such file or directory\npc-1\n",
			want:   map[string]Ownership{"/a": Owned, "/b": Unknown, "/c": Owned},
		},
		{
			name:   "warning lines are not owners",
			output: "warning: Found bdb_ro Packages database while attempting sqlite backend\npa-1\nfile /b is not owned by any package\npc-1\n",
			want:   map[string]Ownership{"/a": Owned, "/b": NotOwned, "/c": Owned},
		},
		{
			name:   "fewer owner lines than paths",
			output: "pa-1\n",
			want:   map[string]Ownership{"/a": Unknown, "/b": Unknown, "/c": Unknown},
		},
		{
			name:   "empty output",
			output: "",
			want:   map[string]Ownership{"/a": Unknown, "/b": Unknown, "/c": Unknown},
		},
		{
			name:   "global error",
			output: "error: cannot open Packages database\n",
			want:   map[string]Ownership{"/a": Unknown, "/b": Unknown, "/c": Unknown},
		},
		{
			name:   "not-owned line naming an unqueried path counts as noise",
			output: "pa-1\npb-1\npc-1\nfile /zzz is not owned by any package\n",
			want:   map[string]Ownership{"/a": Owned, "/b": Owned, "/c": Owned},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRPMQuery(paths, tc.output)
			if len(got) != len(paths) {
				t.Fatalf("got %d entries, want %d", len(got), len(paths))
			}
			for p, own := range tc.want {
				if got[p] != own {
					t.Errorf("%q = %v, want %v (output %q)", p, got[p], own, strings.TrimSpace(tc.output))
				}
			}
		})
	}
}
