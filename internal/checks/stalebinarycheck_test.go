package checks

import (
	"context"
	"strings"
	"testing"

	"sermo/internal/process"
)

func staleBinaryDeps(stale ...process.StaleBinary) Deps {
	return Deps{StaleBinaries: func() []process.StaleBinary { return stale }}
}

func TestStaleBinaryCheckOKWhenNothingReplaced(t *testing.T) {
	c, warn := buildStaleBinaryCheck(base{name: "stale"}, staleBinaryDeps())
	if warn != "" {
		t.Fatalf("unexpected warning: %s", warn)
	}
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("want OK with no stale binaries, got %+v", res)
	}
	if got := res.Data[DataKeyValue]; got != float64(0) {
		t.Fatalf("want value 0, got %v", got)
	}
}

// The message must name the replaced path: that is what tells the operator
// which package was upgraded and what a restart would pick up.
func TestStaleBinaryCheckReportsPathAndCount(t *testing.T) {
	c, _ := buildStaleBinaryCheck(base{name: "stale"}, staleBinaryDeps(
		process.StaleBinary{PID: 1928, Path: "/usr/bin/ovs-vswitchd"},
		process.StaleBinary{PID: 1898, Path: "/usr/bin/ovsdb-server"},
	))
	res := c.Run(context.Background())
	if res.OK {
		t.Fatal("want not OK when a binary was replaced")
	}
	if !strings.Contains(res.Message, "/usr/bin/ovs-vswitchd") || !strings.Contains(res.Message, "/usr/bin/ovsdb-server") {
		t.Fatalf("message must name the replaced binaries, got %q", res.Message)
	}
	if got := res.Data[DataKeyValue]; got != float64(2) {
		t.Fatalf("want value 2, got %v", got)
	}
	if got, _ := res.Data[DataKeyPIDs].(string); got != "1928,1898" {
		t.Fatalf("want both pids, got %q", got)
	}
}

// A single replaced binary is enough to fail the check, whichever way
// discovery surfaced it — the check reads one list and does not care.
func TestStaleBinaryCheckFailsOnASingleReplacedBinary(t *testing.T) {
	c, _ := buildStaleBinaryCheck(base{name: "stale"}, staleBinaryDeps(
		process.StaleBinary{PID: 7, Path: "/usr/bin/x"},
	))
	if res := c.Run(context.Background()); res.OK {
		t.Fatalf("want not OK, got %+v", res)
	}
}

// Duplicate paths collapse and sort, so a service with several processes of the
// same replaced binary does not produce a reshuffling message every cycle.
func TestStaleBinaryCheckDeduplicatesPaths(t *testing.T) {
	c, _ := buildStaleBinaryCheck(base{name: "stale"}, staleBinaryDeps(
		process.StaleBinary{PID: 2, Path: "/usr/bin/b"},
		process.StaleBinary{PID: 1, Path: "/usr/bin/a"},
		process.StaleBinary{PID: 3, Path: "/usr/bin/a"},
	))
	res := c.Run(context.Background())
	if got, _ := res.Data[DataKeyPath].(string); got != "/usr/bin/a,/usr/bin/b" {
		t.Fatalf("want deduplicated sorted paths, got %q", got)
	}
}

func TestStaleBinaryCheckNeedsDiscovery(t *testing.T) {
	if _, warn := buildStaleBinaryCheck(base{name: "stale"}, Deps{}); warn == "" {
		t.Fatal("want a warning when process discovery is unavailable")
	}
}

// An uninstalled package leaves no file at the path: the message must not send
// the operator to a restart that cannot start anything, and the reading still
// fails (it alerts) but blocks every operation, the stale-binary restart among
// them. A replaced binary in the same service keeps its restart remedy.
func TestStaleBinaryCheckReportsAnUninstalledBinaryWithoutARestart(t *testing.T) {
	c, _ := buildStaleBinaryCheck(base{name: "stale"}, staleBinaryDeps(
		process.StaleBinary{PID: 721825, Path: "/usr/bin/apache2", Removed: true},
		process.StaleBinary{PID: 2016594, Path: "/usr/bin/apache2", Removed: true},
	))
	res := c.Run(context.Background())
	if res.OK || !res.ActionBlocked {
		t.Fatalf("want a failing, action-blocked reading, got %+v", res)
	}
	if !strings.Contains(res.Message, "2 process(es) run a binary no longer installed (/usr/bin/apache2); "+StaleBinaryRemovedHint) {
		t.Fatalf("message = %q", res.Message)
	}
	if strings.Contains(res.Message, StaleBinaryRestartHint) {
		t.Fatalf("an uninstalled binary must not suggest a restart: %q", res.Message)
	}

	mixed, _ := buildStaleBinaryCheck(base{name: "stale"}, staleBinaryDeps(
		process.StaleBinary{PID: 1, Path: "/usr/sbin/old", Removed: true},
		process.StaleBinary{PID: 2, Path: "/usr/sbin/upgraded"},
	))
	res = mixed.Run(context.Background())
	if !res.ActionBlocked || !strings.Contains(res.Message, "/usr/sbin/old); "+StaleBinaryRemovedHint) ||
		!strings.Contains(res.Message, "1 process(es) run a replaced binary (/usr/sbin/upgraded); "+StaleBinaryRestartHint) {
		t.Fatalf("mixed result = %+v", res)
	}

	replaced, _ := buildStaleBinaryCheck(base{name: "stale"}, staleBinaryDeps(process.StaleBinary{PID: 3, Path: "/usr/sbin/upgraded"}))
	if res := replaced.Run(context.Background()); res.ActionBlocked {
		t.Fatalf("a replaced binary must keep its restart remedy: %+v", res)
	}
}
