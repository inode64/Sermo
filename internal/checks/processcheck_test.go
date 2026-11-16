package checks

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"sermo/internal/process"
)

// dmeventd on 192.0.2.352 and .55 was running normally from a binary lvm2 had
// replaced on disk. Its exe link resolves to a deleted inode, so the exact-exe
// selector stops matching and the check read "state absent (want running)" —
// sending the operator after a dead daemon that was in fact serving the
// previous version. The message has to say why, and the reading is the same
// verdictless state the stale-binary check reports: the service is serving,
// it needs a restart, and health must not book an outage.
func TestProcessCheckExplainsAReplacedBinaryInsteadOfBareAbsent(t *testing.T) {
	c := processCheck{
		name:       "process",
		exes:       []string{"/usr/bin/dmeventd"},
		user:       "root",
		expect:     process.StateRunning,
		observeAny: observedState(process.StateAbsent),
		stale: func() []process.StaleBinary {
			return []process.StaleBinary{{PID: 3549, Path: "/usr/bin/dmeventd"}}
		},
	}
	res := c.Run(context.Background())
	if res.OK {
		t.Fatal("a replaced binary is still something to act on; the check must not pass")
	}
	if !strings.Contains(res.Message, "state absent (want running)") {
		t.Fatalf("message = %q, want the observed state preserved", res.Message)
	}
	if !strings.Contains(res.Message, "/usr/bin/dmeventd was replaced on disk") {
		t.Fatalf("message = %q, want the replaced binary named", res.Message)
	}
	if got := res.Observation(); got != ObservationNeutral {
		t.Fatalf("observation = %q, want a verdictless state reading for a running replaced binary", got)
	}
	if got := res.Data[DataKeyReplacedBinaries]; got != "/usr/bin/dmeventd" {
		t.Fatalf("data[%s] = %v, want the replaced binary for the state reason", DataKeyReplacedBinaries, got)
	}
}

// An uninstalled daemon still runs from its deleted file. The reading names the
// removal and blocks operations: a restart-on-failure rule must not stop it for
// a start that has nothing to start.
func TestProcessCheckExplainsAnUninstalledBinaryAndBlocksActions(t *testing.T) {
	c := processCheck{
		name:       "process",
		exes:       []string{"/usr/bin/apache2"},
		user:       "root",
		expect:     process.StateRunning,
		observeAny: observedState(process.StateAbsent),
		stale: func() []process.StaleBinary {
			return []process.StaleBinary{{PID: 721825, Path: "/usr/bin/apache2", Removed: true}}
		},
	}
	res := c.Run(context.Background())
	if res.OK || !res.ActionBlocked || res.Reports != ReportsState {
		t.Fatalf("want a failing, action-blocked state reading, got %+v", res)
	}
	if !strings.Contains(res.Message, "/usr/bin/apache2 is no longer installed") || !strings.Contains(res.Message, StaleBinaryRemovedHint) {
		t.Fatalf("message = %q", res.Message)
	}
}

// Catalog backup guards (`active: {check: backup}`) run this check with
// expect absent. An unproven absence must be unavailable so the guard denies,
// never a passing "absent".
// A watch may name a daemon its service does not attribute: Samba's nmbd under a
// replaced binary is alive, not gone, even though the service's own list omits it.
func TestProcessCheckExplainsAReplacedBinaryTheServiceDoesNotAttribute(t *testing.T) {
	c := processCheck{
		name: "nmbd", exes: []string{"/usr/bin/nmbd"}, user: "root", expect: process.StateRunning,
		observeAny:  observedState(process.StateAbsent),
		stale:       func() []process.StaleBinary { return nil },
		replacedAny: func([]string, string) []string { return []string{"/usr/bin/nmbd"} },
	}
	res := c.Run(t.Context())
	if res.Reports != ReportsState || res.Severity.Valid() || !strings.Contains(res.Message, "/usr/bin/nmbd was replaced on disk") {
		t.Fatalf("result = %+v, want the verdictless replaced-binary reading", res)
	}
}

func TestProcessCheckUnknownStateIsUnavailable(t *testing.T) {
	for _, expect := range []string{process.StateAbsent, process.StateRunning} {
		c := processCheck{
			name:       "backup",
			exes:       []string{"/usr/bin/mysqldump"},
			user:       "backup",
			expect:     expect,
			observeAny: observedState(process.StateUnknown),
		}
		res := c.Run(context.Background())
		if !res.Unavailable || res.OK {
			t.Fatalf("expect %s: result = %+v, want unavailable and not OK", expect, res)
		}
	}
}

// A genuinely absent process is an outage, verdict included: the verdictless
// reading is reserved for the replaced-binary case.
func TestProcessCheckKeepsTheVerdictWhenNothingWasReplaced(t *testing.T) {
	c := processCheck{
		name:       "process",
		exes:       []string{"/usr/sbin/nope"},
		expect:     process.StateRunning,
		observeAny: observedState(process.StateAbsent),
		stale:      func() []process.StaleBinary { return nil },
	}
	res := c.Run(context.Background())
	if got := res.Observation(); got != ObservationFailing {
		t.Fatalf("observation = %q, want a failing verdict for a missing process", got)
	}
	if res.Data != nil {
		t.Fatalf("data = %v, want none without a replaced binary", res.Data)
	}
}

// A genuinely absent process keeps the plain message: the explanation must only
// appear when a replaced binary actually accounts for the miss.
func TestProcessCheckKeepsPlainAbsentWhenNothingWasReplaced(t *testing.T) {
	c := processCheck{
		name:       "process",
		exes:       []string{"/usr/sbin/nope"},
		expect:     process.StateRunning,
		observeAny: observedState(process.StateAbsent),
		stale: func() []process.StaleBinary {
			// A replaced binary belonging to a different selector must not be
			// blamed for this check's miss.
			return []process.StaleBinary{{PID: 42, Path: "/usr/bin/other"}}
		},
	}
	res := c.Run(context.Background())
	if res.Message != "state absent (want running)" {
		t.Fatalf("message = %q, want the plain absent reading", res.Message)
	}
}

// A running process reports normally; the stale lookup must not touch the happy
// path.
func TestProcessCheckRunningIsUnaffected(t *testing.T) {
	c := processCheck{
		name:       "process",
		exes:       []string{"/usr/bin/dmeventd"},
		expect:     process.StateRunning,
		observeAny: observedState(process.StateRunning),
		stale:      func() []process.StaleBinary { t.Fatal("stale lookup ran for a healthy process"); return nil },
	}
	if res := c.Run(context.Background()); !res.OK {
		t.Fatalf("running process = %+v, want OK", res)
	}
}

// observedState is an observer that always reads state.
func observedState(state string) ProcessesAnyFunc {
	return func(context.Context, []string, string, *regexp.Regexp) process.StateObservation {
		return process.StateObservation{State: state}
	}
}

// A guard blocks on "not absent" and repeats this message in its event; on a
// production host "state running (want absent)" hid a pretend emerge orphaned for hours.
// The reading names the process, so the operator can inspect or end it.
func TestProcessCheckNamesTheRunningProcess(t *testing.T) {
	for _, tc := range []struct {
		name     string
		observed process.StateObservation
		want     string
	}{
		{
			name:     "single match",
			observed: process.StateObservation{State: process.StateRunning, PID: 1310048, PPID: 1, Program: "emerge"},
			want:     "state running (want absent): pid 1310048 (ppid 1) emerge",
		},
		{
			name:     "unnamed program",
			observed: process.StateObservation{State: process.StateRunning, PID: 9, PPID: 2},
			want:     "state running (want absent): pid 9 (ppid 2)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := processCheck{
				name: "process", cmd: regexp.MustCompile("emerge"), expect: process.StateAbsent,
				observeAny: func(context.Context, []string, string, *regexp.Regexp) process.StateObservation { return tc.observed },
			}
			res := c.Run(context.Background())
			if res.OK || res.Message != tc.want {
				t.Fatalf("result = ok %v %q, want failing %q", res.OK, res.Message, tc.want)
			}
		})
	}
}
