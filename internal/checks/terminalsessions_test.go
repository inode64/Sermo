package checks

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"sermo/internal/execx"
)

type terminalSessionRunner struct {
	result      execx.Result
	err         error
	user        string
	name        string
	args        []string
	hasDeadline bool
}

func (r *terminalSessionRunner) Run(context.Context, string, ...string) (execx.Result, error) {
	return execx.Result{ExitCode: execx.ExitCodeRunFailure}, errors.New("Run must not be used")
}

func (r *terminalSessionRunner) RunUser(ctx context.Context, user, name string, args ...string) (execx.Result, error) {
	_, r.hasDeadline = ctx.Deadline()
	r.user = user
	r.name = name
	r.args = append([]string(nil), args...)
	return r.result, r.err
}

type waitingTerminalSessionRunner struct{}

func (waitingTerminalSessionRunner) Run(context.Context, string, ...string) (execx.Result, error) {
	return execx.Result{ExitCode: execx.ExitCodeRunFailure}, errors.New("Run must not be used")
}

func (waitingTerminalSessionRunner) RunUser(ctx context.Context, _ string, _ string, _ ...string) (execx.Result, error) {
	<-ctx.Done()
	return execx.Result{ExitCode: execx.ExitCodeRunFailure}, ctx.Err()
}

func TestTerminalMultiplexerAdapterParsesSessions(t *testing.T) {
	tests := []struct {
		name    string
		config  TerminalSessionConfig
		output  string
		want    []TerminalSession
		wantErr bool
	}{
		{
			name:   "tmux attached and detached",
			config: TerminalSessionConfig{Multiplexer: TerminalMultiplexerTmux, User: "deploy"},
			output: "build\t1\t2\nops\t0\t1\n",
			want: []TerminalSession{
				{Multiplexer: TerminalMultiplexerTmux, Name: "build", User: "deploy", State: terminalSessionStateAttached, Windows: 2},
				{Multiplexer: TerminalMultiplexerTmux, Name: "ops", User: "deploy", State: terminalSessionStateDetached, Windows: 1},
			},
		},
		{
			name:   "screen states",
			config: TerminalSessionConfig{Multiplexer: TerminalMultiplexerScreen, User: "deploy"},
			output: "There are screens on:\n\t120.ops\t(Detached)\n\t121.build\t(Attached)\n2 Sockets in /run/screen/S-deploy.\n",
			want: []TerminalSession{
				{Multiplexer: TerminalMultiplexerScreen, Name: "120.ops", User: "deploy", State: terminalSessionStateDetached},
				{Multiplexer: TerminalMultiplexerScreen, Name: "121.build", User: "deploy", State: terminalSessionStateAttached},
			},
		},
		{
			name:    "malformed tmux",
			config:  TerminalSessionConfig{Multiplexer: TerminalMultiplexerTmux, User: "deploy"},
			output:  "build\tunknown\t2",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter, ok := terminalMultiplexerAdapterFor(tt.config.Multiplexer)
			if !ok {
				t.Fatalf("terminalMultiplexerAdapterFor(%q) not found", tt.config.Multiplexer)
			}
			got, err := adapter.parseSessions(tt.config.User, tt.output)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseSessions() error = %v, wantErr=%v", err, tt.wantErr)
			}
			if err == nil && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseSessions() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestTerminalSessionsCheckRunsReadOnlyClientAsConfiguredUser(t *testing.T) {
	runner := &terminalSessionRunner{result: execx.Result{ExitCode: execx.ExitCodeSuccess, Stdout: "ops\t1\t3\nbuild\t0\t1\n"}}
	built, warnings := Build(map[string]any{
		"sessions": map[string]any{
			CheckKeyType:        CheckTypeTerminalSessions,
			CheckKeyMultiplexer: TerminalMultiplexerTmux,
			CheckKeyBinary:      "/usr/bin/tmux",
			CheckKeyUser:        "deploy",
			CheckKeyCount:       map[string]any{CheckKeyOp: ">", CheckKeyValue: 0},
		},
	}, Deps{DefaultTimeout: time.Second, Runner: runner})
	if len(warnings) != 0 || len(built) != 1 {
		t.Fatalf("Build() built=%d warnings=%v, want one check", len(built), warnings)
	}

	result := built[0].Check.Run(context.Background())
	if !result.OK || result.Unavailable || !result.Condition {
		t.Fatalf("result = %+v, want available firing condition", result)
	}
	if runner.user != "deploy" || runner.name != "/usr/bin/tmux" || !reflect.DeepEqual(runner.args, []string{"list-sessions", "-F", tmuxSessionFormat}) || !runner.hasDeadline {
		t.Fatalf("RunUser(user=%q, name=%q, args=%v, deadline=%v), want configured tmux query", runner.user, runner.name, runner.args, runner.hasDeadline)
	}
	if result.Data[DataKeyCount] != 2 || result.Data[DataKeyAttached] != 1 || result.Data[DataKeyDetached] != 1 {
		t.Fatalf("result data = %#v", result.Data)
	}
	sessions := TerminalSessionsFromData(result.Data)
	if len(sessions) != 2 || sessions[0].Name != "build" || sessions[1].State != terminalSessionStateAttached {
		t.Fatalf("sessions = %#v, want sorted session data", sessions)
	}
}

func TestTerminalSessionsTreatsKnownEmptyClientOutputAsZero(t *testing.T) {
	tests := []struct {
		name   string
		config TerminalSessionConfig
		result execx.Result
	}{
		{
			name:   "tmux without server",
			config: TerminalSessionConfig{Multiplexer: TerminalMultiplexerTmux, Binary: "/usr/bin/tmux", User: "deploy"},
			result: execx.Result{ExitCode: 1, Stderr: "error connecting to /tmp/tmux-1000/default (No such file or directory)"},
		},
		{
			name:   "screen without sockets",
			config: TerminalSessionConfig{Multiplexer: TerminalMultiplexerScreen, Binary: "/usr/bin/screen", User: "deploy"},
			result: execx.Result{ExitCode: 1, Stdout: "No Sockets found in /run/screen/S-deploy."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sample, err := sampleTerminalSessions(context.Background(), &terminalSessionRunner{result: tt.result}, tt.config)
			if err != nil || len(sample.Sessions) != 0 {
				t.Fatalf("sampleTerminalSessions() = %#v, %v; want empty success", sample, err)
			}
		})
	}
}

func TestTerminalSessionsCheckFailsClosedOnCommandTimeout(t *testing.T) {
	check := terminalSessionsCheck{
		base:   base{name: "sessions", timeout: time.Millisecond},
		preds:  []levelPred{{field: DataKeyCount, op: ">", value: 0}},
		config: TerminalSessionConfig{Multiplexer: TerminalMultiplexerTmux, Binary: "/usr/bin/tmux", User: "deploy"},
		runner: waitingTerminalSessionRunner{},
	}
	result := check.Run(context.Background())
	if result.OK || !result.Unavailable {
		t.Fatalf("timeout result = %+v, want unavailable failure", result)
	}
}

func TestTerminalSessionsFromDataAcceptsJSONHydration(t *testing.T) {
	sessions := TerminalSessionsFromData(map[string]any{DataKeyTerminalSessions: []any{
		map[string]any{"multiplexer": "tmux", "name": "ops", "user": "deploy", "state": "attached", "windows": float64(2)},
		map[string]any{"multiplexer": "screen", "name": "120.backup", "user": "backup", "state": "detached"},
		map[string]any{"multiplexer": "unknown", "name": "skip", "user": "root", "state": "attached"},
	}})
	want := []TerminalSession{
		{Multiplexer: TerminalMultiplexerTmux, Name: "ops", User: "deploy", State: terminalSessionStateAttached, Windows: 2},
		{Multiplexer: TerminalMultiplexerScreen, Name: "120.backup", User: "backup", State: terminalSessionStateDetached},
	}
	if !reflect.DeepEqual(sessions, want) {
		t.Fatalf("TerminalSessionsFromData() = %#v, want %#v", sessions, want)
	}
}
