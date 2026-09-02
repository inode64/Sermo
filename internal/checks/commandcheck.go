package checks

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"sermo/internal/execx"
	"sermo/internal/output"
)

// cmdState persists on_change state while a service worker or host watch reuses
// the check instance across cycles. A config reload/worker rebuild creates a
// fresh baseline.
type cmdState struct {
	primed  bool
	last    string // comparison key (raw line, or version_short truncated to a level)
	lastRaw string // the raw line behind `last`, shown in change messages
}

// commandCheck runs a command and compares its exit code, and optionally its
// stdout/stderr, to expectations. With on_change it also alerts when
// the command's stdout changes between cycles (e.g. a `version` command whose
// output changed). The command is always an argv array, never a shell string
// .
type commandCheck struct {
	base
	runner     execx.Runner
	argv       []string
	user       string
	expectExit []int
	stdout     OutputMatcher
	stderr     OutputMatcher
	version    VersionMatcher
	exports    []commandExport
	analyzer   *outputAnalyzer
	onChange   bool
	// changeLevel selects how on_change compares output: 0 compares the trimmed
	// raw line; 1/2/3 compare version_short truncated to that many components
	// (major/minor/patch), with a raw-line fallback when no version is parseable.
	changeLevel int
	state       *cmdState
	// numeric marks a check whose entry declares a `unit:`: the first numeric
	// token of stdout publishes as DataKeyValue, feeding the `value` graph
	// series DeclaredGraphMetrics offers for any check with a unit. Output that
	// carries no leading number simply publishes no sample (a graph gap), never
	// a failure — the command's exit code and matchers stay the verdict.
	numeric bool
}

func (c commandCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	start := run.start

	res, err := runCheckCommand(ctx, c.runner, c.user, c.argv)
	// fail builds a failing result and attaches the bounded command output so the
	// emitted event can show WHY the command failed.
	fail := func(msg string) Result {
		r := c.result(false, msg, start)
		if out := output.Bounded(res.Stdout, res.Stderr); out != "" {
			r.Data = map[string]any{DataKeyOutput: out}
		}
		return r
	}
	if res.ExitCode == execx.ExitCodeRunFailure {
		msg := execx.OperatorFailureOr(err, res, c.timeout, execx.CommandDidNotStart)
		r := fail(msg)
		r.Unavailable = true
		return r
	}
	if !ExitCodeExpected(res.ExitCode, c.expectExit) {
		msg := fmt.Sprintf("exit %d (want %s)", res.ExitCode, ExpectExitText(c.expectExit))
		if stderr := output.FirstNonEmptyLine(res.Stderr); stderr != "" {
			msg += ": " + stderr
		} else if err != nil {
			msg += ": " + err.Error()
		}
		return fail(msg)
	}
	if ok, detail := c.stdout.Match(res.Stdout); !ok {
		return fail(fmt.Sprintf("exit %d; stdout %s", res.ExitCode, detail))
	}
	if ok, detail := c.stderr.Match(res.Stderr); !ok {
		return fail(fmt.Sprintf("exit %d; stderr %s", res.ExitCode, detail))
	}
	if ok, detail := c.version.Match(VersionOutput(res.Stdout, res.Stderr)); !ok {
		return fail(fmt.Sprintf("exit %d; version %s", res.ExitCode, detail))
	}
	if c.analyzer.Active() {
		if sev, id, line := c.analyzer.Analyze(res.Stdout, res.Stderr); sev != SevOK {
			r := c.result(false, fmt.Sprintf("exit %d; %s pattern %q: %s", res.ExitCode, sev, id, output.FirstNonEmptyLine(line)), start)
			r.Optional = sev == SevWarning
			r.Data = map[string]any{DataKeyPatternID: id, DataKeyPatternSeverity: sev.String(), DataKeyPatternLine: line}
			if out := output.Bounded(res.Stdout, res.Stderr); out != "" {
				r.Data[DataKeyOutput] = out
			}
			return r
		}
	}
	if c.onChange && c.state != nil {
		raw := strings.TrimSpace(res.Stdout)
		key := c.changeKey(raw)
		if c.state.primed && key != c.state.last {
			r := c.result(false, fmt.Sprintf("output changed (%s -> %s)", output.FirstNonEmptyLine(c.state.lastRaw), output.FirstNonEmptyLine(raw)), start)
			r.Data = map[string]any{DataKeyOld: c.state.lastRaw, DataKeyNew: raw}
			c.state.last, c.state.lastRaw = key, raw
			return r
		}
		c.state.last, c.state.lastRaw, c.state.primed = key, raw, true
	}
	r := c.result(true, fmt.Sprintf("exit %d (want %s)", res.ExitCode, ExpectExitText(c.expectExit)), start)
	data := c.exportData(res.Stdout, res.Stderr)
	if c.numeric {
		if v, ok := firstNumericToken(res.Stdout); ok {
			if data == nil {
				data = map[string]any{}
			}
			data[DataKeyValue] = v
			// Only the first line joins the message: a verbose command with a
			// unit must not bloat every event with its whole stdout.
			r.Message = fmt.Sprintf("%s: %s", output.FirstNonEmptyLine(res.Stdout), r.Message)
		}
	}
	if len(data) > 0 {
		r.Data = data
	}
	return r
}

// firstNumericToken parses the first whitespace-separated token of a command's
// stdout as a float. `exim -bpc` prints "17"; a wrapped tool may prefix it.
func firstNumericToken(stdout string) (float64, bool) {
	fields := strings.Fields(stdout)
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// changeKey turns a trimmed command output into the value on_change compares
// across cycles. With changeLevel==0 the raw line is the key. With a level set
// (version monitor), the key is version_short truncated to that level so only a
// change at or above the chosen granularity fires; when no version is parseable
// it falls back to the raw line so a change is never silently missed.
func (c commandCheck) changeKey(raw string) string {
	if c.changeLevel <= 0 {
		return raw
	}
	if short := TruncateVersion(ShortVersion(raw), c.changeLevel); short != "" {
		return short
	}
	return raw
}

// runCheckCommand runs a check's argv, switching to user when configured.
func runCheckCommand(ctx context.Context, runner execx.Runner, user string, argv []string) (execx.Result, error) {
	if user != "" {
		res, err := execx.RunUser(ctx, runner, execx.NoTimeout, user, argv[0], argv[1:]...)
		if err != nil {
			return res, fmt.Errorf("run command: %w", err)
		}
		return res, nil
	}
	res, err := runner.Run(ctx, argv[0], argv[1:]...)
	if err != nil {
		return res, fmt.Errorf("run command: %w", err)
	}
	return res, nil
}

// ExitCodeExpected reports whether got matches one of the expected command exit
// codes. A nil or empty expected list means the default success code, 0.
func ExitCodeExpected(got int, want []int) bool {
	if len(want) == 0 {
		want = []int{CommandDefaultExpectedExit}
	}
	return slices.Contains(want, got)
}

// ExpectExitText formats expected exit codes for operator-facing messages.
func ExpectExitText(want []int) string {
	if len(want) == 0 {
		return strconv.Itoa(CommandDefaultExpectedExit)
	}
	parts := make([]string, 0, len(want))
	for _, n := range want {
		parts = append(parts, strconv.Itoa(n))
	}
	return strings.Join(parts, " or ")
}

func (c commandCheck) exportData(stdout, stderr string) map[string]any {
	if len(c.exports) == 0 {
		return nil
	}
	out := make(map[string]any, len(c.exports))
	for _, e := range c.exports {
		out[e.name] = e.value(stdout, stderr)
	}
	return out
}
