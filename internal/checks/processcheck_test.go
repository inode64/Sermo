package checks

import (
	"context"
	"strings"
	"testing"

	"sermo/internal/process"
)

// dmeventd on 192.0.2.352 and .55 was running normally from a binary lvm2 had
// replaced on disk. Its exe link resolves to a deleted inode, so the exact-exe
// selector stops matching and the check read "state absent (want running)" —
// sending the operator after a dead daemon that was in fact serving the
// previous version. The verdict stays a failure; the message has to say why.
func TestProcessCheckExplainsAReplacedBinaryInsteadOfBareAbsent(t *testing.T) {
	c := processCheck{
		name:    "process",
		exes:    []string{"/usr/bin/dmeventd"},
		user:    "root",
		expect:  process.StateRunning,
		observe: func(string, string) string { return process.StateAbsent },
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
}

// A genuinely absent process keeps the plain message: the explanation must only
// appear when a replaced binary actually accounts for the miss.
func TestProcessCheckKeepsPlainAbsentWhenNothingWasReplaced(t *testing.T) {
	c := processCheck{
		name:    "process",
		exes:    []string{"/usr/sbin/nope"},
		expect:  process.StateRunning,
		observe: func(string, string) string { return process.StateAbsent },
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
		name:    "process",
		exes:    []string{"/usr/bin/dmeventd"},
		expect:  process.StateRunning,
		observe: func(string, string) string { return process.StateRunning },
		stale:   func() []process.StaleBinary { t.Fatal("stale lookup ran for a healthy process"); return nil },
	}
	if res := c.Run(context.Background()); !res.OK {
		t.Fatalf("running process = %+v, want OK", res)
	}
}
