// Package execxtest provides the scripted execx.Runner shared by unit tests.
//
// A Runner answers each call from its most specific script entry (exact
// command line, then executable name, then the next queued result, then
// Default) and records every call, so a test asserts what ran, in which
// order and with which environment or user without spawning a process.
package execxtest

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"sermo/internal/execx"
)

// Call is one recorded invocation.
type Call struct {
	Name string
	Args []string
	// Env is the environment passed to RunEnv; nil for Run and RunUser.
	Env []string
	// User is the account passed to RunUser; empty otherwise.
	User string
}

// Line returns the call's command line in the form ByLine and Count use.
func (c Call) Line() string { return Line(c.Name, c.Args...) }

// Line joins an executable and its arguments with single spaces.
func Line(name string, args ...string) string {
	if len(args) == 0 {
		return name
	}
	return name + " " + strings.Join(args, " ")
}

// Runner is a scripted execx.Runner. The zero value answers every call with a
// successful empty result. It is safe for concurrent use.
type Runner struct {
	// ByLine answers a call whose full command line matches exactly.
	ByLine map[string]execx.Result
	// ByName answers a call by executable name when ByLine has no entry.
	ByName map[string]execx.Result
	// Queue answers calls in order when neither map matches; the last entry
	// repeats once the queue is exhausted.
	Queue []execx.Result
	// Default answers when nothing else matches.
	Default execx.Result
	// Errs maps a command line or executable name to the error returned with
	// that call's result, whether or not a result entry matched.
	Errs map[string]error
	// Err is returned with Default when no entry and no Errs key matched.
	Err error
	// RespectContext returns ctx.Err() instead of an answer when the context
	// is already done, as the real runner does.
	RespectContext bool

	mu          sync.Mutex
	calls       []Call
	next        int
	sawDeadline bool
}

// RunOnly exposes only Run from a Runner, for tests that prove a caller fails
// closed when its runner cannot switch user or environment.
type RunOnly struct {
	Runner *Runner
}

// Run implements execx.Runner.
func (r RunOnly) Run(ctx context.Context, name string, args ...string) (execx.Result, error) {
	return r.Runner.Run(ctx, name, args...)
}

// Fixed returns a Runner that answers every call with res and err.
func Fixed(res execx.Result, err error) *Runner {
	return &Runner{Default: res, Err: err}
}

// Queued returns a Runner that answers calls with results in order, repeating
// the last one.
func Queued(results ...execx.Result) *Runner {
	return &Runner{Queue: results}
}

// Outputs returns a Runner that answers calls with successful results carrying
// each stdout in order, repeating the last one.
func Outputs(stdouts ...string) *Runner {
	results := make([]execx.Result, 0, len(stdouts))
	for _, out := range stdouts {
		results = append(results, execx.Result{Stdout: out})
	}
	return Queued(results...)
}

// Run implements execx.Runner.
func (r *Runner) Run(ctx context.Context, name string, args ...string) (execx.Result, error) {
	return r.answer(ctx, Call{Name: name, Args: args})
}

// RunEnv implements execx.EnvRunner.
func (r *Runner) RunEnv(ctx context.Context, env []string, name string, args ...string) (execx.Result, error) {
	return r.answer(ctx, Call{Name: name, Args: args, Env: env})
}

// RunUser implements execx.UserRunner.
func (r *Runner) RunUser(ctx context.Context, user, name string, args ...string) (execx.Result, error) {
	return r.answer(ctx, Call{Name: name, Args: args, User: user})
}

func (r *Runner) answer(ctx context.Context, call Call) (execx.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	call.Args = append([]string(nil), call.Args...)
	r.calls = append(r.calls, call)
	if _, ok := ctx.Deadline(); ok {
		r.sawDeadline = true
	}
	if r.RespectContext {
		if err := ctx.Err(); err != nil {
			return execx.Result{}, fmt.Errorf("execxtest: %s refused: %w", call.Line(), err)
		}
	}
	line := call.Line()
	err := r.Errs[line]
	if err == nil {
		err = r.Errs[call.Name]
	}
	if res, ok := r.ByLine[line]; ok {
		return res, err
	}
	if res, ok := r.ByName[call.Name]; ok {
		return res, err
	}
	if len(r.Queue) > 0 {
		res := r.Queue[min(r.next, len(r.Queue)-1)]
		r.next++
		return res, err
	}
	if err == nil {
		err = r.Err
	}
	return r.Default, err
}

// Calls returns every recorded call in order.
func (r *Runner) Calls() []Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Call(nil), r.calls...)
}

// Lines returns the recorded command lines in order.
func (r *Runner) Lines() []string {
	calls := r.Calls()
	lines := make([]string, 0, len(calls))
	for _, c := range calls {
		lines = append(lines, c.Line())
	}
	return lines
}

// Count reports how many recorded calls match the command line exactly.
func (r *Runner) Count(name string, args ...string) int {
	want := Line(name, args...)
	n := 0
	for _, c := range r.Calls() {
		if c.Line() == want {
			n++
		}
	}
	return n
}

// Ran reports whether any recorded call ran name.
func (r *Runner) Ran(name string) bool {
	for _, c := range r.Calls() {
		if c.Name == name {
			return true
		}
	}
	return false
}

// SawDeadline reports whether any recorded call carried a context deadline.
func (r *Runner) SawDeadline() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sawDeadline
}
