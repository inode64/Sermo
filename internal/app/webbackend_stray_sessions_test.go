package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/process"
	"sermo/internal/web"
)

func TestStraySessionsUseFreshPublishedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		age      time.Duration
		disabled bool
		want     int
	}{
		{"fresh", 0, false, 1},
		{"stale", time.Hour, false, 0},
		{"disabled", 0, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := straysSnapshotBackend(t, 1, tc.age)
			entry := b.entries["web"]
			entry.disabled = tc.disabled
			proc := process.Process{PID: 42, PPID: 1, UID: 0, StartTicks: 100, Stray: true, ExePrev: "/usr/bin/screen-old"}
			b.snapshots.publishWithCheckTypes("web", map[string]checks.Result{
				"strays": {Check: "strays", Data: map[string]any{checks.DataKeyProcesses: []process.Process{proc}}},
			}, map[string]bool{"strays": true}, map[string]string{"strays": checks.CheckTypeStrays})
			bindServiceSnapshots(entry, b.snapshots, "web")
			now := time.Now()
			b.now = func() time.Time { return now.Add(tc.age) }
			inventory := b.Sessions(context.Background())
			if len(inventory.Strays) != tc.want {
				t.Fatalf("strays=%+v", inventory.Strays)
			}
			if tc.want > 0 {
				row := inventory.Strays[0]
				if row.ExePrevious != proc.ExePrev || row.CanClose || row.CloseReason == "" || row.Identity != process.SignalIdentity(proc) {
					t.Fatalf("missing executable or authorization evidence: %+v", row)
				}
			}
		})
	}
}

func TestStrayTerminalAssociation(t *testing.T) {
	for _, shared := range []bool{false, true} {
		b := &WebBackend{entries: map[string]*webEntry{"ssh": {terminalSessions: []terminalSessionSource{{check: "tmux", binary: "/usr/bin/tmux"}}}}}
		snapshot := sessionProcessSnapshot{byPID: map[int]process.Identity{
			10: {PID: 10, PPID: 1, Exe: "/usr/bin/tmux", ExeOK: true, StartTicks: 100},
			11: {PID: 11, PPID: 10, StartTicks: 101}, 12: {PID: 12, PPID: 11, StartTicks: 102},
			20: {PID: 20, PPID: 10, StartTicks: 103},
		}, children: map[int][]int{10: {11, 20}, 11: {12}}}
		inv := web.SessionInventory{
			Terminal: []web.TerminalSession{{Service: "ssh", Check: "tmux", Multiplexer: "tmux", Name: "main", Identity: "$0:100", PIDs: []int{11}, CanClose: true}},
			Strays:   []web.StraySession{{Service: "ssh", PID: 10, StartTicks: 100}, {Service: "ssh", PID: 12, StartTicks: 102}, {Service: "ssh", PID: 20, StartTicks: 999}},
		}
		if shared {
			inv.Terminal = append(inv.Terminal, web.TerminalSession{Service: "ssh", Check: "tmux", Name: "other", Identity: "$1:100", PIDs: []int{20}})
		}
		b.attachStrayTerminals(&inv, snapshot)
		if (inv.Strays[0].Terminal == nil) != shared {
			t.Fatalf("shared=%v server association=%+v", shared, inv.Strays[0])
		}
		if inv.Strays[1].Terminal == nil || inv.Strays[1].Terminal.Name != "main" || inv.Strays[1].CanClose {
			t.Fatalf("association grants reap or missing: %+v", inv.Strays[1])
		}
		if inv.Strays[2].Terminal != nil {
			t.Fatal("recycled PID associated")
		}
	}
}
