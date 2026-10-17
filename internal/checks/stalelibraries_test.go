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
			c := &staleLibrariesCheck{stableFor: time.Minute, clock: func() time.Time { return now }, since: now.Add(-time.Hour), previous: []process.StaleLibrary{lib}}
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
			if !r.ActionBlocked || r.OK {
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
