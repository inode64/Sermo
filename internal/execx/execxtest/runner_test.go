package execxtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"sermo/internal/execx"
)

func TestRunnerAnswersFromMostSpecificEntry(t *testing.T) {
	errName := errors.New("name failed")
	r := &Runner{
		ByLine:  map[string]execx.Result{"ls -l": {Stdout: "line"}},
		ByName:  map[string]execx.Result{"ls": {Stdout: "name"}},
		Queue:   []execx.Result{{Stdout: "q1"}, {Stdout: "q2"}},
		Default: execx.Result{ExitCode: 127},
		Errs:    map[string]error{"ls": errName},
		Err:     errors.New("default"),
	}
	tests := []struct {
		name    string
		exe     string
		args    []string
		want    string
		wantErr error
	}{
		{name: "exact line wins", exe: "ls", args: []string{"-l"}, want: "line", wantErr: errName},
		{name: "name fallback", exe: "ls", args: []string{"-a"}, want: "name", wantErr: errName},
		{name: "queue first", exe: "cat", want: "q1"},
		{name: "queue second", exe: "cat", want: "q2"},
		{name: "queue repeats last", exe: "cat", want: "q2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := r.Run(context.Background(), tc.exe, tc.args...)
			if res.Stdout != tc.want || !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %q/%v, want %q/%v", res.Stdout, err, tc.want, tc.wantErr)
			}
		})
	}
	if got := r.Count("ls", "-l"); got != 1 {
		t.Fatalf("Count(ls -l) = %d, want 1", got)
	}
	if !r.Ran("cat") || r.Ran("rm") {
		t.Fatalf("Ran misreported: cat=%v rm=%v", r.Ran("cat"), r.Ran("rm"))
	}
	if lines := r.Lines(); len(lines) != 5 || lines[0] != "ls -l" {
		t.Fatalf("Lines = %v", lines)
	}
}

func TestRunnerDefaultAndFixed(t *testing.T) {
	errDefault := errors.New("default")
	r := Fixed(execx.Result{ExitCode: 2}, errDefault)
	res, err := r.Run(context.Background(), "missing")
	if res.ExitCode != 2 || !errors.Is(err, errDefault) {
		t.Fatalf("Fixed answered %+v/%v", res, err)
	}
	zero := &Runner{}
	if res, err := zero.Run(context.Background(), "anything"); err != nil || res.ExitCode != 0 {
		t.Fatalf("zero Runner answered %+v/%v", res, err)
	}
	q := Queued(execx.Result{Stdout: "a"})
	if res, _ := q.Run(context.Background(), "x"); res.Stdout != "a" {
		t.Fatalf("Queued answered %+v", res)
	}
	o := Outputs("first", "second")
	first, _ := o.Run(context.Background(), "x")
	second, _ := o.Run(context.Background(), "x")
	third, _ := o.Run(context.Background(), "x")
	if first.Stdout != "first" || second.Stdout != "second" || third.Stdout != "second" {
		t.Fatalf("Outputs answered %q %q %q", first.Stdout, second.Stdout, third.Stdout)
	}
}

func TestRunnerRecordsEnvUserAndDeadline(t *testing.T) {
	r := &Runner{}
	if _, err := r.RunEnv(context.Background(), []string{"A=1"}, "hook", "arg"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RunUser(context.Background(), "postgres", "psql"); err != nil {
		t.Fatal(err)
	}
	if r.SawDeadline() {
		t.Fatal("deadline seen before any bounded call")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := r.Run(ctx, "bounded"); err != nil {
		t.Fatal(err)
	}
	calls := r.Calls()
	if len(calls) != 3 || calls[0].Env[0] != "A=1" || calls[0].Args[0] != "arg" || calls[1].User != "postgres" {
		t.Fatalf("calls = %+v", calls)
	}
	if !r.SawDeadline() {
		t.Fatal("deadline not recorded")
	}
}

func TestRunOnlyHidesUserAndEnv(t *testing.T) {
	inner := Fixed(execx.Result{Stdout: "ok"}, nil)
	var runner execx.Runner = RunOnly{Runner: inner}
	if _, ok := runner.(execx.UserRunner); ok {
		t.Fatal("RunOnly must not satisfy execx.UserRunner")
	}
	if _, ok := runner.(execx.EnvRunner); ok {
		t.Fatal("RunOnly must not satisfy execx.EnvRunner")
	}
	if res, err := runner.Run(context.Background(), "x"); err != nil || res.Stdout != "ok" || len(inner.Calls()) != 1 {
		t.Fatalf("RunOnly.Run answered %+v/%v with %d recorded calls", res, err, len(inner.Calls()))
	}
}

func TestRunnerRespectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &Runner{ByName: map[string]execx.Result{"x": {Stdout: "answered"}}}
	if res, err := r.Run(ctx, "x"); err != nil || res.Stdout != "answered" {
		t.Fatalf("without RespectContext got %+v/%v", res, err)
	}
	r.RespectContext = true
	if _, err := r.Run(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("with RespectContext got %v, want context.Canceled", err)
	}
	if len(r.Calls()) != 2 {
		t.Fatalf("calls = %d, want 2 (a refused call is still recorded)", len(r.Calls()))
	}
}
