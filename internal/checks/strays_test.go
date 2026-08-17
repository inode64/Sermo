package checks

import (
	"context"
	"strings"
	"testing"

	"sermo/internal/process"
)

func straysDeps(strays ...process.Process) Deps {
	return Deps{Strays: func() []process.Process { return strays }}
}

func stray(pid int, exe string) process.Process {
	return process.Process{
		PID: pid, PPID: 1, UID: 0, User: "root",
		Exe: exe, ExeOK: true,
		Role: process.RoleMain, Source: process.SourceBackend, Stray: true,
	}
}

func TestStraysCheckOKWhenNothingUnaccountedFor(t *testing.T) {
	c, warn := buildStraysCheck(base{name: straysCheckTestName}, straysDeps())
	if warn != "" {
		t.Fatalf("unexpected warning: %s", warn)
	}
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("want OK with no strays, got %+v", res)
	}
	if got := res.Data[DataKeyValue]; got != float64(0) {
		t.Fatalf("want value 0, got %v", got)
	}
}

const straysCheckTestName = "strays"

// The message must name the executables: the count alone says something
// accumulated, the names say what — the difference between an alert and a
// diagnosis.
func TestStraysCheckReportsExecutablesAndCount(t *testing.T) {
	c, _ := buildStraysCheck(base{name: straysCheckTestName}, straysDeps(
		stray(163544, "/usr/bin/node"),
		stray(90697, "/usr/bin/dbus-daemon"),
	))
	res := c.Run(context.Background())
	if res.OK {
		t.Fatal("want not OK when strays exist")
	}
	for _, want := range []string{"/usr/bin/node", "/usr/bin/dbus-daemon", "sermoctl reap"} {
		if !strings.Contains(res.Message, want) {
			t.Fatalf("message %q must contain %q", res.Message, want)
		}
	}
	if got := res.Data[DataKeyValue]; got != float64(2) {
		t.Fatalf("want value 2, got %v", got)
	}
	if got, _ := res.Data[DataKeyPIDs].(string); got != "163544,90697" {
		t.Fatalf("want both pids, got %q", got)
	}
}

// A delegated process is the service's workload, kept alive on purpose. It must
// never be counted here, even if classification handed it over.
func TestStraysCheckExcludesDelegated(t *testing.T) {
	delegated := stray(300, "/usr/sbin/glusterfsd")
	delegated.Delegated = true
	c, _ := buildStraysCheck(base{name: straysCheckTestName}, straysDeps(delegated))
	if res := c.Run(context.Background()); !res.OK {
		t.Fatalf("want OK: a delegated process is not a stray, got %+v", res)
	}
}

// Duplicate executables collapse and sort, so a service leaking several copies of
// one binary does not produce a reshuffling message every cycle.
func TestStraysCheckDeduplicatesExecutables(t *testing.T) {
	c, _ := buildStraysCheck(base{name: straysCheckTestName}, straysDeps(
		stray(2, "/usr/bin/b"),
		stray(1, "/usr/bin/a"),
		stray(3, "/usr/bin/a"),
	))
	res := c.Run(context.Background())
	if got, _ := res.Data[DataKeyPath].(string); got != "/usr/bin/a,/usr/bin/b" {
		t.Fatalf("want deduplicated sorted executables, got %q", got)
	}
}

// An unidentifiable process is exactly what this check exists to surface, so it
// must never drop out of the list for want of a resolvable exe.
func TestStraysCheckNamesProcessesWithoutAResolvableExe(t *testing.T) {
	unresolved := stray(400, "")
	unresolved.ExeOK = false
	unresolved.Cmdline = []string{"PM2", "God", "Daemon"}
	replaced := stray(401, "")
	replaced.ExeOK = false
	replaced.ExePrev = "/usr/bin/old"
	blank := stray(402, "")
	blank.ExeOK = false

	c, _ := buildStraysCheck(base{name: straysCheckTestName}, straysDeps(unresolved, replaced, blank))
	res := c.Run(context.Background())
	got, _ := res.Data[DataKeyPath].(string)
	for _, want := range []string{"PM2 God Daemon", "/usr/bin/old", strayUnknownExecutable} {
		if !strings.Contains(got, want) {
			t.Fatalf("executables %q must contain %q", got, want)
		}
	}
	if res.Data[DataKeyValue] != float64(3) {
		t.Fatalf("want all three counted, got %v", res.Data[DataKeyValue])
	}
}

// The count is graphed so a slow leak shows as a rising series; process_count
// moves with legitimate load too, so this is the series that says "unexplained".
func TestStraysCheckPublishesAGraphableCount(t *testing.T) {
	declared := GraphMetrics(CheckTypeStrays)
	if len(declared) != 1 || declared[0].Key != DataKeyCount {
		t.Fatalf("graph metrics = %+v, want one on %q", declared, DataKeyCount)
	}
	c, _ := buildStraysCheck(base{name: straysCheckTestName}, straysDeps(stray(1, "/usr/bin/a")))
	res := c.Run(context.Background())
	if _, ok := res.Data[declared[0].Key]; !ok {
		t.Fatalf("result data %v lacks the graphed key %q", res.Data, declared[0].Key)
	}
}

func TestStraysCheckNeedsDiscovery(t *testing.T) {
	if _, warn := buildStraysCheck(base{name: straysCheckTestName}, Deps{}); warn == "" {
		t.Fatal("want a warning when process discovery is unavailable")
	}
}
