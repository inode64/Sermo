package app

import (
	"context"
	"slices"
	"strings"
	"testing"

	"sermo/internal/checks"
	"sermo/internal/execx/execxtest"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/web"
)

func TestResidualScreenInventoryAndFreshCloseIdentity(t *testing.T) {
	const identity = "residual:120:1200:1000:1:99"
	session := checks.TerminalSession{
		Multiplexer: checks.TerminalMultiplexerScreen, Name: "120.residual", User: "1000",
		State: checks.TerminalSessionStateResidual, Identity: identity, PIDs: []int{120},
	}
	snaps := NewSnapshots()
	snaps.publishWithCheckTypes("web", map[string]checks.Result{
		"screen": {Check: "screen", OK: true, Data: map[string]any{checks.DataKeyTerminalSessions: []checks.TerminalSession{session}}},
	}, map[string]bool{"screen": true}, map[string]string{"screen": checks.CheckTypeTerminalSessions})
	b := webBackendWithEntry(snaps, []string{"screen"}, map[string]string{"screen": checks.CheckTypeTerminalSessions})
	source := terminalSessionSource{check: "screen", multiplexer: checks.TerminalMultiplexerScreen, user: "1000", binary: "/usr/bin/screen-4.9.1"}
	b.entries["web"].terminalSessions = []terminalSessionSource{source}
	inventory := b.Sessions(t.Context())
	if len(inventory.Terminal) != 1 || !inventory.Terminal[0].CanClose || inventory.Terminal[0].State != checks.TerminalSessionStateResidual || inventory.Sources[0].State != web.SessionSourceAvailable {
		t.Fatalf("residual must offer close: %+v", inventory)
	}
	proc := process.Process{PID: 120, UID: 1000, StartTicks: 1200, ExePrev: source.binary, ExeFile: process.ExecutableFile{Device: 1, Inode: 99}}
	reads := 0
	verify := freshTerminalSessionVerifier(func(_ context.Context, config checks.TerminalSessionConfig) ([]process.Process, error) {
		reads++
		if config.Binary != source.binary || config.User != source.user {
			t.Fatalf("config=%+v", config)
		}
		return []process.Process{proc}, nil
	}, []terminalSessionSource{source})
	target := operation.TerminalSessionTarget{Check: source.check, Multiplexer: source.multiplexer, Name: session.Name, User: source.user, Identity: identity}
	if got, err := verify(t.Context(), target); err != nil || got.PID != proc.PID || got.ExeFile != proc.ExeFile {
		t.Fatalf("target=%+v err=%v", got, err)
	}
	proc.StartTicks++
	if _, err := verify(t.Context(), target); err == nil {
		t.Fatal("accepted a reused PID")
	}
	if reads != 2 {
		t.Fatalf("verification reads=%d, want one fresh sample per close", reads)
	}
}

func TestTerminalSessionActionsRejectChangedSource(t *testing.T) {
	source := terminalSessionSource{check: "screen", multiplexer: checks.TerminalMultiplexerScreen, user: "1000", binary: "/usr/bin/screen"}
	for _, tc := range []struct {
		name   string
		change func(*operation.TerminalSessionTarget)
	}{
		{name: "missing source", change: func(target *operation.TerminalSessionTarget) { target.Check = "missing" }},
		{name: "changed multiplexer", change: func(target *operation.TerminalSessionTarget) { target.Multiplexer = checks.TerminalMultiplexerTmux }},
		{name: "changed user", change: func(target *operation.TerminalSessionTarget) { target.User = "0" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := operation.TerminalSessionTarget{Check: source.check, Multiplexer: source.multiplexer, Name: "120.ops", User: source.user, Identity: "120:1200"}
			tc.change(&target)
			runner := &execxtest.Runner{}
			closeSession := freshTerminalSessionCloser(runner, []terminalSessionSource{source})
			if err := closeSession(t.Context(), target); err == nil || !strings.Contains(err.Error(), "source changed") || len(runner.Calls()) != 0 {
				t.Fatalf("client close: err=%v calls=%v", err, runner.Calls())
			}
			verify := freshTerminalSessionVerifier(func(context.Context, checks.TerminalSessionConfig) ([]process.Process, error) {
				t.Fatal("a mismatched source must be rejected before sampling processes")
				return nil, nil
			}, []terminalSessionSource{source})
			if _, err := verify(t.Context(), target); err == nil || !strings.Contains(err.Error(), "source changed") {
				t.Fatalf("residual close: err=%v", err)
			}
		})
	}
}

func TestTerminalSessionCloserUsesConfiguredNamespaceAndDisplayedIdentity(t *testing.T) {
	source := terminalSessionSource{check: "tmux", multiplexer: checks.TerminalMultiplexerTmux, user: "1000", binary: "/usr/bin/tmux", socket: "/run/user/1000/tmux.sock"}
	for _, identity := range []string{"$7:90", "$7:89"} {
		t.Run(identity, func(t *testing.T) {
			runner := execxtest.Outputs("ops\t0\t1\t100\t$7\t90\t201\t/dev/pts/1\n", "")
			closeSession := freshTerminalSessionCloser(runner, []terminalSessionSource{source})
			err := closeSession(t.Context(), operation.TerminalSessionTarget{
				Check: source.check, Multiplexer: source.multiplexer, Name: "ops", User: source.user, Identity: identity,
			})
			wantClose := identity == "$7:90"
			calls := runner.Calls()
			if (err == nil) != wantClose || wantClose && len(calls) != 2 || !wantClose && len(calls) != 1 {
				t.Fatalf("err=%v calls=%v", err, calls)
			}
			for _, call := range calls {
				socketIndex := slices.Index(call.Args, "-S")
				if call.User != source.user || call.Name != source.binary || socketIndex < 0 || socketIndex+1 >= len(call.Args) || call.Args[socketIndex+1] != source.socket {
					t.Fatalf("client escaped configured namespace: %+v", call)
				}
			}
			if wantClose && !slices.Equal(calls[1].Args[2:], []string{"kill-session", "-t", "=ops"}) {
				t.Fatalf("close command=%v", calls[1])
			}
		})
	}
}
