package checks

import (
	"context"
	"errors"
	"testing"
	"time"

	"sermo/internal/process"
)

func TestStaleLibrariesStableWindowAndCache(t *testing.T) {
	now := time.Unix(1000, 0)
	calls := 0
	report := process.LibraryReport{Processes: 1, Libraries: []process.StaleLibrary{{PID: 42, StartTicks: 100, Path: "/lib/libpam.so.0", RestartEligible: true, Installed: process.ExecutableFile{Inode: 2}}}}
	var scanErr error
	c := &staleLibrariesCheck{stableFor: 5 * time.Minute, clock: func() time.Time { return now }, sample: func(context.Context) (process.LibraryReport, error) { calls++; return report, scanErr }}
	if r := c.Run(t.Context()); r.OK || !r.ActionBlocked {
		t.Fatalf("first scan: %+v", r)
	}
	now = now.Add(30 * time.Second)
	_ = c.Run(t.Context())
	if calls != 1 {
		t.Fatal("rescanned before interval")
	}
	now = now.Add(5 * time.Minute)
	if r := c.Run(t.Context()); r.OK || r.ActionBlocked {
		t.Fatalf("stable replacement: %+v", r)
	}
	report.Libraries[0].Installed.Inode++
	now = now.Add(time.Minute)
	if r := c.Run(t.Context()); !r.ActionBlocked {
		t.Fatal("new update did not reset stability")
	}
	now = now.Add(5 * time.Minute)
	scanErr = errors.New("permission denied")
	if r := c.Run(t.Context()); !r.ActionBlocked || !r.Unavailable || r.OK {
		t.Fatalf("incomplete scan: %+v", r)
	}
	now = now.Add(time.Minute)
	scanErr = nil
	if r := c.Run(t.Context()); !r.ActionBlocked {
		t.Fatal("read failure did not reset stability")
	}
	now = now.Add(5 * time.Minute)
	report.Libraries = nil
	if r := c.Run(t.Context()); !r.OK || r.ActionBlocked {
		t.Fatalf("recovery: %+v", r)
	}
}

// A forked worker inherits the replaced mappings of its main process, so a busy
// service changes its PIDs and its worker count without any update under way.
func TestStaleLibrariesStableForFollowsFilesAndMainProcess(t *testing.T) {
	principal := process.StaleLibrary{PID: 10, StartTicks: 100, Exe: "/usr/bin/daemon", Path: "/lib/libcrypt.so.2", Mapped: process.ExecutableFile{Inode: 1},
		Installed: process.ExecutableFile{Inode: 2}, Modified: 5, Changed: 6, RestartEligible: true}
	worker := func(pid int) process.StaleLibrary {
		lib := principal
		lib.PID, lib.StartTicks, lib.UID, lib.RestartEligible = pid, uint64(pid), 81, false
		return lib
	}
	replacedAgain := func(lib process.StaleLibrary) process.StaleLibrary {
		lib.Installed.Inode++
		return lib
	}
	touched := func(lib process.StaleLibrary) process.StaleLibrary {
		lib.Changed++
		return lib
	}
	otherFile := worker(20)
	otherFile.Path = "/lib/libssl.so.3"
	restarted := func(pid int, ticks uint64) process.StaleLibrary {
		lib := principal
		lib.PID, lib.StartTicks = pid, ticks
		return lib
	}
	advisory := principal
	advisory.RestartEligible = false

	type scans = [][]process.StaleLibrary
	for name, tc := range map[string]struct {
		samples     scans
		wantBlocked bool
	}{
		"worker replaced": {samples: scans{{principal, worker(20)}, {principal, worker(21)}}},
		"worker exited":   {samples: scans{{principal, worker(20)}, {principal}}},
		"worker appeared": {samples: scans{{principal}, {principal, worker(20), worker(21)}}},

		"main process replaced":       {samples: scans{{principal}, {restarted(11, 200)}}, wantBlocked: true},
		"main process PID reused":     {samples: scans{{principal}, {restarted(10, 200)}}, wantBlocked: true},
		"file replaced again":         {samples: scans{{principal}, {replacedAgain(principal)}}, wantBlocked: true},
		"file touched":                {samples: scans{{principal}, {touched(principal)}}, wantBlocked: true},
		"worker file replaced":        {samples: scans{{principal, worker(20)}, {principal, replacedAgain(worker(20))}}, wantBlocked: true},
		"worker file touched":         {samples: scans{{principal, worker(20)}, {principal, touched(worker(20))}}, wantBlocked: true},
		"worker file added":           {samples: scans{{principal, worker(20)}, {principal, worker(20), otherFile}}, wantBlocked: true},
		"main process eligible again": {samples: scans{{principal}, {advisory}, {principal}}, wantBlocked: true},
	} {
		t.Run(name, func(t *testing.T) {
			now := time.Unix(1000, 0)
			var libraries []process.StaleLibrary
			c := &staleLibrariesCheck{stableFor: 5 * time.Minute, clock: func() time.Time { return now }, sample: func(context.Context) (process.LibraryReport, error) {
				return process.LibraryReport{Processes: len(libraries), Libraries: libraries}, nil
			}}
			var r Result
			for _, libraries = range tc.samples {
				r = c.Run(t.Context())
				now = now.Add(6 * time.Minute)
			}
			if r.ActionBlocked != tc.wantBlocked {
				t.Fatalf("ActionBlocked = %t, want %t: %s", r.ActionBlocked, tc.wantBlocked, r.Message)
			}
		})
	}
}

func TestStaleLibrariesRefreshesDependenciesAndReportsStopped(t *testing.T) {
	now := time.Unix(1000, 0)
	report := process.LibraryReport{Processes: 1, Dependencies: []string{"/lib/one.so"}}
	c := &staleLibrariesCheck{clock: func() time.Time { return now }, sample: func(context.Context) (process.LibraryReport, error) { return report, nil }}
	first := c.Run(t.Context())
	if first.Data[DataKeyComplete] != true {
		t.Fatal(first)
	}
	report.Dependencies = []string{"/lib/one.so", "/lib/plugin.so"}
	now = now.Add(time.Minute)
	if result := c.Run(t.Context()); len(result.Data[DataKeyDependencies].([]string)) != 2 {
		t.Fatal("late module missing from refreshed inventory")
	}
	report = process.LibraryReport{}
	now = now.Add(time.Minute)
	if result := c.Run(t.Context()); !result.Unavailable || !result.ActionBlocked || result.Data[DataKeyComplete] != false {
		t.Fatalf("stopped service reported empty success: %+v", result)
	}
}

func TestStaleLibrariesAdvisoryCases(t *testing.T) {
	for _, name := range []string{"host", "child", "missing", "cancelled", "concurrent"} {
		t.Run(name, func(t *testing.T) {
			now := time.Unix(1000, 0)
			lib := process.StaleLibrary{PID: 42, Path: "/lib/libpam.so.0", RestartEligible: true}
			if name == "child" {
				lib.RestartEligible = false
			}
			if name == "missing" {
				lib.Missing = true
			}
			c := &staleLibrariesCheck{stableFor: time.Minute, clock: func() time.Time { return now }, since: now.Add(-time.Hour), previous: libraryEvidence([]process.StaleLibrary{lib})}
			if name != "host" {
				c.sample = func(context.Context) (process.LibraryReport, error) {
					return process.LibraryReport{Libraries: []process.StaleLibrary{lib}}, nil
				}
			}
			var r Result
			switch name {
			case "host":
				r = c.libraryResult(process.LibraryReport{Libraries: []process.StaleLibrary{lib}}, nil, now, now)
			case "cancelled":
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				r = c.Run(ctx)
			case "concurrent":
				c.mu.Lock()
				r = c.Run(t.Context())
				c.mu.Unlock()
			default:
				r = c.Run(t.Context())
			}
			// Unsafe is a result that could drive an action: one not blocked,
			// or one claiming a complete clean inventory.
			if !r.ActionBlocked || (r.OK && !r.Unavailable) {
				t.Fatalf("unsafe result: %+v", r)
			}
		})
	}
}

func TestBuildStaleLibraries(t *testing.T) {
	for _, raw := range []any{"0s", "bad", -1, true} {
		if _, issue := buildStaleLibrariesCheck(base{}, map[string]any{CheckKeyStableFor: raw}, Deps{}); issue == "" {
			t.Fatalf("accepted %v", raw)
		}
	}
	if _, issue := buildStaleLibrariesCheck(base{}, nil, Deps{Service: "sshd"}); issue == "" {
		t.Fatal("service silently became host inventory")
	}
	c, issue := buildStaleLibrariesCheck(base{}, nil, Deps{})
	if issue != "" {
		t.Fatal(issue)
	}
	if metadata := checkResultMetadata(c); metadata.Reports != ReportsState {
		t.Fatalf("default must not count stale libraries as outage: %+v", metadata)
	}
}

func TestStaleLibrariesExecutionFailureStaysAdvisory(t *testing.T) {
	c := &staleLibrariesCheck{clock: time.Now, stableFor: time.Minute, sample: func(context.Context) (process.LibraryReport, error) { panic("fixture") }}
	if r := Execute(t.Context(), c); !r.Unavailable || !r.ActionBlocked {
		t.Fatalf("panic authorized an action: %+v", r)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	limited := withLimiter(c, NewLimiter(1), time.Second)
	if r := Execute(ctx, limited); !r.Unavailable || !r.ActionBlocked {
		t.Fatalf("cancelled queue authorized an action: %+v", r)
	}
}

// An inventory that a recycled worker left incomplete proves only what it
// found: no stale mapping states no failure (an alert must not read it as
// replaced libraries), a stale one stays evidence; neither may drive an action.
func TestStaleLibrariesIncompleteInventoryStatesOnlyItsFindings(t *testing.T) {
	now := time.Unix(1000, 0)
	errChurn := errors.New("scan service libraries: pid 42: process identity changed or cannot be verified")
	for _, tc := range []struct {
		name   string
		libs   []process.StaleLibrary
		wantOK bool
	}{
		{"nothing found", nil, true},
		{"stale found", []process.StaleLibrary{{PID: 7, Path: "/lib/libssl.so.3", RestartEligible: true}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &staleLibrariesCheck{stableFor: time.Minute, clock: func() time.Time { return now }, sample: func(context.Context) (process.LibraryReport, error) {
				return process.LibraryReport{Processes: 12, Libraries: tc.libs}, errChurn
			}}
			r := c.Run(t.Context())
			if r.OK != tc.wantOK || !r.Unavailable || !r.ActionBlocked || r.Data[DataKeyComplete] != false {
				t.Fatalf("result = %+v, want OK=%t, unavailable and blocked", r, tc.wantOK)
			}
		})
	}
}
