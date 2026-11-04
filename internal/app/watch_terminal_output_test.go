package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/emission"
	"sermo/internal/process"
	"sermo/internal/rules"
)

func TestWatchTerminalOutputRecoveryPolicyAndSafetyModes(t *testing.T) {
	for _, name := range []string{"success", "changed evidence", "failure", "dry run", "panic", "advisory", "unavailable", "serialized payload"} {
		t.Run(name, func(t *testing.T) {
			at := time.Unix(1000, 0)
			calls := 0
			var events []Event
			result := checks.Result{OK: false, Data: map[string]any{checks.DataKeyTerminalOutput: []process.TerminalOutput{{Owner: process.OutputIdentity{PID: 42}}}}}
			w := &Watch{Name: "output", CheckType: checks.CheckTypeTerminalOutput, FireOnFail: true, DrainOutput: &DrainOutputSpec{LogFile: "/var/log/output.log"}, Policy: rules.Policy{Cooldown: time.Minute}, Now: func() time.Time { return at }, Emit: func(e Event) { events = append(events, e) }, Emission: emission.Policy{Events: emission.ModeEveryCycle}, OutputDrainer: func(context.Context, process.TerminalOutput, string) (process.OutputDrain, error) {
				calls++
				r := process.OutputDrain{Bytes: 100, LogFile: "/var/log/output.log"}
				if name == "changed evidence" {
					return r, process.ErrTerminalOutputChanged
				}
				if name == "failure" {
					return r, errors.New("cannot reserve log space")
				}
				return r, nil
			}}
			switch name {
			case "dry run":
				w.DryRun = true
			case "panic":
				w.InPanic = func() bool { return true }
			case "advisory":
				result.ActionBlocked = true
			case "unavailable":
				result.Unavailable = true
				result.ActionBlocked = true
			case "serialized payload":
				result.Data[checks.DataKeyTerminalOutput] = []any{map[string]any{"pid": 42}}
			}
			w.Check = &scriptedCheck{results: []checks.Result{result, result, result}}
			w.RunCycle(t.Context())
			w.RunCycle(t.Context())
			shouldAct := name == "success" || name == "failure" || name == "changed evidence"
			if shouldAct && calls != 1 {
				t.Fatalf("calls=%d; cooldown was not honored", calls)
			}
			if !shouldAct && calls != 0 {
				t.Fatalf("unsafe mode called repair %d times", calls)
			}
			kind := eventKindOutputDrain
			if name == "failure" {
				kind = eventKindOutputDrainFailed
			}
			if name == "changed evidence" {
				kind = eventKindOutputDrainSkipped
			}
			if shouldAct && !hasEventKind(events, kind) {
				t.Fatalf("missing audit %s: %+v", kind, events)
			}
			at = at.Add(2 * time.Minute)
			w.RunCycle(t.Context())
			if shouldAct && calls != 2 {
				t.Fatal("eligible recovery never retried after cooldown")
			}
		})
	}
}

func TestBuildWatchesTerminalOutputRequiresCooldown(t *testing.T) {
	for _, cooldown := range []string{"", "0s", "5m"} {
		t.Run(cooldown, func(t *testing.T) {
			entry := map[string]any{
				"check": map[string]any{"type": "terminal_output", "processes": map[string]any{"main": map[string]any{"exe": "/usr/bin/driver", "user": "root"}}},
				"then":  map[string]any{"drain_output": map[string]any{"log_file": "/var/log/sermo/output.log"}},
			}
			if cooldown != "" {
				entry["policy"] = map[string]any{"cooldown": cooldown}
			}
			watches, warnings := BuildWatches(cfgWithWatches(map[string]any{"output": entry}), Deps{}, time.Minute)
			if cooldown == "5m" {
				if len(warnings) != 0 || len(watches) != 1 || watches[0].OutputDrainer == nil {
					t.Fatalf("valid recovery not wired: watches=%+v warnings=%v", watches, warnings)
				}
			} else if len(warnings) == 0 || len(watches) != 0 {
				t.Fatalf("unpaced recovery accepted: watches=%+v warnings=%v", watches, warnings)
			}
		})
	}
}

func TestServiceTerminalOutputConstrainedToFreshPrincipals(t *testing.T) {
	for _, name := range []string{"principal", "foreign", "delegated", "recycled", "changed cgroup", "changed executable"} {
		t.Run(name, func(t *testing.T) {
			id := process.Identity{PID: 42, PPID: 1, Exe: "/usr/bin/driver", ExeOK: true, StartTicks: 100, StartTicksOK: true, UID: 0, Cgroup: "0::/service"}
			ids := serviceOwnershipReader{42: id}
			selectors := []process.Selector{{Name: process.RoleMain, Type: process.SelectorCommandMatch, Exe: id.Exe, User: "root"}}
			if name == "delegated" {
				selectors[0].Delegated = true
			}
			d := process.Discoverer{Reader: ids, ResolveUser: func(string) (uint32, bool) { return 0, true }}
			target := process.TerminalOutput{Owner: process.OutputIdentity{PID: id.PID, StartTicks: id.StartTicks, Exe: id.Exe, UID: id.UID, Cgroup: id.Cgroup}}
			sample := func(context.Context, []process.Selector) ([]process.TerminalOutput, error) {
				switch name {
				case "foreign":
					target.Owner.PID = 99
				case "recycled":
					id.StartTicks++
				case "changed cgroup":
					id.Cgroup = "0::/other"
				case "changed executable":
					id.Exe = "/usr/bin/other"
				}
				ids[42] = id
				return []process.TerminalOutput{target}, nil
			}
			got, err := serviceTerminalOutput(d, selectors, sample)(t.Context(), selectors)
			if err != nil || (len(got) == 1) != (name == "principal") {
				t.Fatalf("service ownership escaped: targets=%+v err=%v", got, err)
			}
		})
	}
}
