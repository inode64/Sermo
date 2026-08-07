package checks

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"sermo/internal/execx"
)

type waitingTerminalSessionRunner struct{}

type terminalSessionSequenceRunner struct {
	results []execx.Result
	calls   [][]string
}

func (r *terminalSessionSequenceRunner) Run(context.Context, string, ...string) (execx.Result, error) {
	return execx.Result{ExitCode: execx.ExitCodeRunFailure}, errors.New("Run must not be used")
}

func (r *terminalSessionSequenceRunner) RunUser(_ context.Context, user, name string, args ...string) (execx.Result, error) {
	r.calls = append(r.calls, append([]string{user, name}, args...))
	if len(r.results) == 0 {
		return execx.Result{ExitCode: execx.ExitCodeRunFailure}, errors.New("unexpected call")
	}
	result := r.results[0]
	r.results = r.results[1:]
	return result, nil
}

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
			name:   "tmux multiple clients and detached",
			config: TerminalSessionConfig{Multiplexer: TerminalMultiplexerTmux, User: "deploy"},
			output: "build\t2\t2\t100\t$1\t90\t201\t/dev/pts/1\nops\t0\t1\t200\t$2\t180\t202\t/dev/pts/2\n",
			want: []TerminalSession{
				{Multiplexer: TerminalMultiplexerTmux, Name: "build", User: "deploy", State: TerminalSessionStateAttached, Windows: 2, ActivityUnix: 100, Identity: "$1:90", PIDs: []int{201}, TTY: "/dev/pts/1"},
				{Multiplexer: TerminalMultiplexerTmux, Name: "ops", User: "deploy", State: TerminalSessionStateDetached, Windows: 1, ActivityUnix: 200, Identity: "$2:180", PIDs: []int{202}, TTY: "/dev/pts/2"},
			},
		},
		{
			name:   "screen states",
			config: TerminalSessionConfig{Multiplexer: TerminalMultiplexerScreen, User: "deploy", StartTicks: func(pid int) (uint64, bool) { return uint64(pid * 10), true }},
			output: "There are screens on:\n\t120.ops\t(Detached)\n\t121.build\t(Attached)\n\t122.pair\t(Multi, attached)\n\t123.batch\t(Multi, detached)\n4 Sockets in /run/screen/S-deploy.\n",
			want: []TerminalSession{
				{Multiplexer: TerminalMultiplexerScreen, Name: "120.ops", User: "deploy", State: TerminalSessionStateDetached, Identity: "120:1200", PIDs: []int{120}},
				{Multiplexer: TerminalMultiplexerScreen, Name: "121.build", User: "deploy", State: TerminalSessionStateAttached, Identity: "121:1210", PIDs: []int{121}},
				{Multiplexer: TerminalMultiplexerScreen, Name: "122.pair", User: "deploy", State: TerminalSessionStateAttached, Identity: "122:1220", PIDs: []int{122}},
				{Multiplexer: TerminalMultiplexerScreen, Name: "123.batch", User: "deploy", State: TerminalSessionStateDetached, Identity: "123:1230", PIDs: []int{123}},
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
			got, err := adapter.parseSessions(tt.config, tt.output)
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
	runner := &recordingUserRunner{result: execx.Result{ExitCode: execx.ExitCodeSuccess, Stdout: "ops\t1\t3\t100\t$1\t90\t201\t/dev/pts/1\nbuild\t0\t1\t200\t$2\t180\t202\t/dev/pts/2\n"}}
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
	if result.Data[DataKeyCount] != 2 || result.Data[DataKeyAttached] != 1 || result.Data[DataKeyDetached] != 1 || result.Data[DataKeyPresent] != true {
		t.Fatalf("result data = %#v", result.Data)
	}
	sessions := TerminalSessionsFromData(result.Data)
	if len(sessions) != 2 || sessions[0].Name != "build" || sessions[1].State != TerminalSessionStateAttached {
		t.Fatalf("sessions = %#v, want sorted session data", sessions)
	}
}

func TestTerminalSessionsTreatsKnownEmptyClientOutputAsZero(t *testing.T) {
	tests := []struct {
		name        string
		config      TerminalSessionConfig
		result      execx.Result
		wantPresent bool
	}{
		{
			name:        "tmux without server",
			config:      TerminalSessionConfig{Multiplexer: TerminalMultiplexerTmux, Binary: "/usr/bin/tmux", User: "deploy"},
			result:      execx.Result{ExitCode: 1, Stderr: "error connecting to /tmp/tmux-1000/default (No such file or directory)"},
			wantPresent: false,
		},
		{
			name:        "screen without sockets",
			config:      TerminalSessionConfig{Multiplexer: TerminalMultiplexerScreen, Binary: "/usr/bin/screen", User: "deploy"},
			result:      execx.Result{ExitCode: 1, Stdout: "No Sockets found in /run/screen/S-deploy."},
			wantPresent: false,
		},
		{
			name:        "live tmux server without sessions",
			config:      TerminalSessionConfig{Multiplexer: TerminalMultiplexerTmux, Binary: "/usr/bin/tmux", User: "deploy"},
			result:      execx.Result{ExitCode: execx.ExitCodeSuccess},
			wantPresent: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sample, err := sampleTerminalSessions(context.Background(), &recordingUserRunner{result: tt.result}, tt.config)
			if err != nil || len(sample.Sessions) != 0 || sample.Present != tt.wantPresent {
				t.Fatalf("sampleTerminalSessions() = %#v, %v; want empty success", sample, err)
			}
		})
	}
}

func TestTerminalSessionsCommandFailureCannotMasqueradeAsEmpty(t *testing.T) {
	check := terminalSessionsCheck{
		base:   base{name: "sessions", timeout: time.Second},
		preds:  []levelPred{{field: DataKeyCount, op: ">", value: 0}},
		config: TerminalSessionConfig{Multiplexer: TerminalMultiplexerScreen, Binary: "/usr/bin/screen", User: "deploy"},
		runner: &recordingUserRunner{
			result: execx.Result{ExitCode: execx.ExitCodeRunFailure, Stderr: "No Sockets found in /run/screen/S-deploy."},
			err:    errors.New("switch user failed"),
		},
	}
	result := check.Run(context.Background())
	if !result.Unavailable {
		t.Fatalf("command failure result = %+v, want unavailable failure", result)
	}
}

func TestTerminalSessionsFromDataAcceptsJSONHydration(t *testing.T) {
	sessions := TerminalSessionsFromData(map[string]any{DataKeyTerminalSessions: []any{
		map[string]any{CheckKeyMultiplexer: TerminalMultiplexerTmux, CheckKeyName: "ops", CheckKeyUser: "deploy", CheckKeyState: TerminalSessionStateAttached, DataKeyWindows: float64(2)},
		map[string]any{CheckKeyMultiplexer: TerminalMultiplexerScreen, CheckKeyName: "120.backup", CheckKeyUser: "backup", CheckKeyState: TerminalSessionStateDetached},
		map[string]any{CheckKeyMultiplexer: "unknown", CheckKeyName: "skip", CheckKeyUser: "root", CheckKeyState: TerminalSessionStateAttached},
	}})
	want := []TerminalSession{
		{Multiplexer: TerminalMultiplexerTmux, Name: "ops", User: "deploy", State: TerminalSessionStateAttached, Windows: 2},
		{Multiplexer: TerminalMultiplexerScreen, Name: "120.backup", User: "backup", State: TerminalSessionStateDetached},
	}
	if !reflect.DeepEqual(sessions, want) {
		t.Fatalf("TerminalSessionsFromData() = %#v, want %#v", sessions, want)
	}
}

func TestCloseTerminalSessionRevalidatesIdentityBeforeExactTmuxClose(t *testing.T) {
	line := "ops\t0\t1\t100\t$7\t90\t201\t/dev/pts/1\n"
	runner := &terminalSessionSequenceRunner{results: []execx.Result{
		{ExitCode: execx.ExitCodeSuccess, Stdout: line},
		{ExitCode: execx.ExitCodeSuccess},
	}}
	config := TerminalSessionConfig{Multiplexer: TerminalMultiplexerTmux, Binary: "/usr/bin/tmux", User: "deploy", Socket: "/run/user/1000/tmux.sock"}
	err := CloseTerminalSession(context.Background(), runner, config, TerminalSession{
		Multiplexer: TerminalMultiplexerTmux, Name: "ops", User: "deploy", Identity: "$7:90",
	})
	if err != nil {
		t.Fatalf("CloseTerminalSession() error = %v", err)
	}
	wantClose := []string{"deploy", "/usr/bin/tmux", "-S", "/run/user/1000/tmux.sock", "kill-session", "-t", "=ops"}
	if len(runner.calls) != 2 || !reflect.DeepEqual(runner.calls[1], wantClose) {
		t.Fatalf("calls = %#v, want exact close %#v", runner.calls, wantClose)
	}
}

func TestCloseTerminalSessionRejectsChangedGenerationWithoutCloseCommand(t *testing.T) {
	runner := &terminalSessionSequenceRunner{results: []execx.Result{{
		ExitCode: execx.ExitCodeSuccess,
		Stdout:   "ops\t0\t1\t100\t$8\t91\t201\t/dev/pts/1\n",
	}}}
	config := TerminalSessionConfig{Multiplexer: TerminalMultiplexerTmux, Binary: "/usr/bin/tmux", User: "deploy"}
	err := CloseTerminalSession(context.Background(), runner, config, TerminalSession{
		Multiplexer: TerminalMultiplexerTmux, Name: "ops", User: "deploy", Identity: "$7:90",
	})
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("CloseTerminalSession() error = %v, want changed generation", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %#v, want list only", runner.calls)
	}
}

func TestCloseTerminalSessionUsesExactScreenClientTarget(t *testing.T) {
	runner := &terminalSessionSequenceRunner{results: []execx.Result{
		{ExitCode: execx.ExitCodeSuccess, Stdout: "\t120.ops\t(Detached)\n"},
		{ExitCode: execx.ExitCodeSuccess},
	}}
	config := TerminalSessionConfig{
		Multiplexer: TerminalMultiplexerScreen, Binary: "/usr/bin/screen", User: "deploy",
		StartTicks: func(pid int) (uint64, bool) { return uint64(pid * 10), true },
	}
	err := CloseTerminalSession(context.Background(), runner, config, TerminalSession{
		Multiplexer: TerminalMultiplexerScreen, Name: "120.ops", User: "deploy", Identity: "120:1200",
	})
	if err != nil {
		t.Fatalf("CloseTerminalSession() error = %v", err)
	}
	wantClose := []string{"deploy", "/usr/bin/screen", "-S", "120.ops", "-X", "quit"}
	if len(runner.calls) != 2 || !reflect.DeepEqual(runner.calls[1], wantClose) {
		t.Fatalf("calls = %#v, want exact close %#v", runner.calls, wantClose)
	}
}
