package checks

import (
	"context"
	"errors"
	"testing"
	"time"

	"sermo/internal/process"
)

func TestTerminalOutputRequiresUnchangedEvidenceAndCompleteReads(t *testing.T) {
	now := time.Unix(1000, 0)
	calls := 0
	target := process.TerminalOutput{Owner: process.OutputIdentity{PID: 42, StartTicks: 100, Exe: "/usr/bin/driver", UID: 0}, Writer: process.OutputIdentity{PID: 44, StartTicks: 102, Exe: "/usr/bin/writer"}, MasterFD: 9, WriterFD: 2, TTYIndex: 11}
	var sampleErr error
	var targets = []process.TerminalOutput{target}
	c := &terminalOutputCheck{clock: func() time.Time { return now }, stableFor: 5 * time.Minute, since: map[process.TerminalOutput]time.Time{}, sample: func(context.Context, []process.Selector) ([]process.TerminalOutput, error) {
		calls++
		return targets, sampleErr
	}}
	if r := c.Run(t.Context()); r.OK || !r.ActionBlocked {
		t.Fatalf("initial=%+v", r)
	}
	now = now.Add(30 * time.Second)
	_ = c.Run(t.Context())
	if calls != 1 {
		t.Fatal("scan interval was bypassed")
	}
	now = now.Add(5 * time.Minute)
	if r := c.Run(t.Context()); r.OK || r.ActionBlocked || len(r.Data[DataKeyTerminalOutput].([]process.TerminalOutput)) != 1 {
		t.Fatalf("stable=%+v", r)
	}
	for _, name := range []string{"progress", "owner generation", "writer generation", "terminal"} {
		switch name {
		case "progress":
			targets[0].WriterWriteChars++
		case "owner generation":
			targets[0].Owner.StartTicks++
		case "writer generation":
			targets[0].Writer.StartTicks++
		case "terminal":
			targets[0].TTYIndex++
		}
		now = now.Add(time.Minute)
		if r := c.Run(t.Context()); !r.ActionBlocked {
			t.Fatalf("%s did not reset evidence", name)
		}
		now = now.Add(5 * time.Minute)
		if r := c.Run(t.Context()); r.ActionBlocked {
			t.Fatalf("unchanged %s never became eligible", name)
		}
	}
	sampleErr = errors.New("permission denied")
	now = now.Add(time.Minute)
	if r := c.Run(t.Context()); !r.Unavailable || !r.ActionBlocked {
		t.Fatalf("incomplete=%+v", r)
	}
	sampleErr = nil
	now = now.Add(time.Minute)
	if r := c.Run(t.Context()); !r.ActionBlocked {
		t.Fatal("uncertainty preserved old authority")
	}
	now = now.Add(5 * time.Minute)
	_ = c.Run(t.Context())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if r := c.Run(ctx); !r.ActionBlocked || !r.Unavailable {
		t.Fatalf("cancelled=%+v", r)
	}
	now = now.Add(time.Minute)
	if r := c.Run(t.Context()); !r.ActionBlocked {
		t.Fatal("cancelled observation preserved stability")
	}
	targets = nil
	now = now.Add(time.Minute)
	if r := c.Run(t.Context()); !r.OK {
		t.Fatalf("recovered=%+v", r)
	}
}

func TestTerminalOutputConfigRequiresExplicitStrictOwnership(t *testing.T) {
	for _, name := range []string{"valid", "no selectors", "no exe", "no user", "delegated", "bad regex", "zero window", "invalid window"} {
		t.Run(name, func(t *testing.T) {
			selector := map[string]any{"exe": "/usr/bin/fixture-driver", "user": "root"}
			entry := map[string]any{CheckKeyProcesses: map[string]any{"main": selector}}
			switch name {
			case "no selectors":
				delete(entry, CheckKeyProcesses)
			case "no exe":
				delete(selector, "exe")
			case "no user":
				delete(selector, "user")
			case "delegated":
				selector["delegated"] = true
			case "bad regex":
				selector["cmd"] = "["
			case "zero window":
				entry[CheckKeyStableFor] = "0s"
			case "invalid window":
				entry[CheckKeyStableFor] = "eventually"
			}
			_, _, err := TerminalOutputConfig(entry)
			if (err == nil) != (name == "valid") {
				t.Fatalf("configuration error=%v", err)
			}
		})
	}
}
