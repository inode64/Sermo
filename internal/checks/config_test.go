package checks

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sermo/internal/execx"
)

// seqRunner returns a stdout/result the test can change between Run calls, to
// drive on_change across cycles (as a watch reuses one built check).
type seqRunner struct{ res execx.Result }

func (r *seqRunner) Run(context.Context, string, ...string) (execx.Result, error) { return r.res, nil }

func TestCommandCheckOnChange(t *testing.T) {
	rr := &seqRunner{res: execx.Result{Stdout: "apache 2.4.57\n"}}
	c := commandCheck{base: base{name: "v", timeout: time.Second}, runner: rr, argv: []string{"apachectl", "-v"}, onChange: true, state: &cmdState{}}

	if res := c.Run(context.Background()); !res.OK {
		t.Fatalf("first cycle primes, should be ok: %s", res.Message)
	}
	if res := c.Run(context.Background()); !res.OK {
		t.Fatalf("unchanged output must stay ok: %s", res.Message)
	}
	rr.res = execx.Result{Stdout: "apache 2.4.58\n"} // version changed
	res := c.Run(context.Background())
	if res.OK {
		t.Fatal("a changed version output must alert (OK=false)")
	}
	if res.Data["new"] != "apache 2.4.58" {
		t.Errorf("change data = %v", res.Data)
	}
}

func TestConfigCheckValidity(t *testing.T) {
	bad := configCheck{base: base{name: "c", timeout: time.Second}, runner: fakeRunner{execx.Result{ExitCode: 1, Stderr: "syntax error on line 7\n"}}, argv: []string{"nginx", "-t"}}
	if res := bad.Run(context.Background()); res.OK {
		t.Error("a failing config test must alert")
	} else if !strings.Contains(res.Message, "syntax error on line 7") {
		t.Errorf("invalid-config message = %q, want the first stderr line included", res.Message)
	}
	good := configCheck{base: base{name: "c", timeout: time.Second}, runner: fakeRunner{execx.Result{ExitCode: 0}}, argv: []string{"nginx", "-t"}}
	if res := good.Run(context.Background()); !res.OK {
		t.Errorf("a passing config test should be ok: %s", res.Message)
	}
	// ExitCode -1 means the command never started: a distinct did-not-start
	// failure, not an ordinary non-zero exit.
	notStarted := configCheck{base: base{name: "c", timeout: time.Second}, runner: fakeRunner{execx.Result{ExitCode: -1}}, argv: []string{"nginx", "-t"}}
	if res := notStarted.Run(context.Background()); res.OK || !strings.Contains(res.Message, execx.CommandDidNotStart) {
		t.Errorf("did-not-start message = %q, want it to contain %q", res.Message, execx.CommandDidNotStart)
	}
}

func TestConfigCheckCommandUser(t *testing.T) {
	runner := &recordingUserRunner{result: execx.Result{ExitCode: 0}}
	check := configCheck{
		base:   base{name: "c", timeout: time.Second},
		runner: runner,
		argv:   []string{"postgres", "--check"},
		user:   "postgres",
	}

	if res := check.Run(context.Background()); !res.OK {
		t.Fatalf("config check with user should pass: %s", res.Message)
	}
	if runner.user != "postgres" || runner.name != "postgres" || len(runner.args) != 1 || runner.args[0] != "--check" {
		t.Fatalf("RunUser call = user=%q name=%q args=%v", runner.user, runner.name, runner.args)
	}
}

func TestConfigCheckChange(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "nginx.conf")
	writeFile(t, f, "a\n")
	c := configCheck{base: base{name: "c", timeout: time.Second}, paths: []string{f}, onChange: true, state: &cmdState{}}

	if res := c.Run(context.Background()); !res.OK {
		t.Fatalf("first cycle primes, should be ok: %s", res.Message)
	}
	writeFile(t, f, "abc\n") // size changes
	if res := c.Run(context.Background()); res.OK {
		t.Error("a changed config file must alert")
	}
}
