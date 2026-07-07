package checks

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"sermo/internal/execx"
	"sermo/internal/output"
)

// hdparmCheck compares configured hdparm timing rates with thresholds. It is
// condition-style: OK means every predicate holds. Only timings used by
// predicates are run; hdparm -t needs root and adds device I/O.
type hdparmCheck struct {
	base
	runner execx.Runner
	device string
	preds  []levelPred
}

func (c hdparmCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	// Run only the timings the predicates need (-T cached, -t buffered): a
	// cached-only check must not pay the slow, I/O-heavy buffered read.
	want := map[string]bool{}
	for _, p := range c.preds {
		want[p.field] = true
	}
	res, runErr := c.runner.Run(ctx, "hdparm", hdparmArgs(c.device, want[fieldCached], want[fieldRead])...)
	if res.ExitCode == -1 {
		msg := execx.OperatorFailure(runErr, res, c.timeout)
		if msg == "" {
			msg = execx.CommandDidNotStart
		}
		return c.result(false, "hdparm "+c.device+": "+msg, start)
	}
	values, err := parseHdparm(res.Stdout)
	if err != nil {
		if s := output.FirstNonEmptyLine(res.Stderr); s != "" {
			return c.result(false, "hdparm "+c.device+": "+s, start)
		}
		return c.result(false, "hdparm "+c.device+": "+err.Error(), start)
	}

	ok := levelPredsHold(c.preds, values)

	r := c.result(ok, hdparmMessage(c.device, values), start)
	r.Data = map[string]any{DataKeyDevice: c.device}
	for k, v := range values {
		r.Data[k] = v
	}
	return r
}

// SampleHdparm runs hdparm -t and/or -T on device and returns MB/s rates.
// timeout is used for operator-facing timeout messages when the probe context
// expires before the command finishes.
func SampleHdparm(ctx context.Context, runner execx.Runner, device string, wantCached, wantRead bool, timeout time.Duration) (map[string]float64, error) {
	if runner == nil {
		runner = execx.CommandRunner{}
	}
	if !wantCached && !wantRead {
		wantCached, wantRead = true, true
	}
	res, runErr := runner.Run(ctx, "hdparm", hdparmArgs(device, wantCached, wantRead)...)
	if res.ExitCode == -1 {
		msg := execx.OperatorFailure(runErr, res, timeout)
		if msg == "" {
			msg = execx.CommandDidNotStart
		}
		return nil, errors.New(msg)
	}
	values, err := parseHdparm(res.Stdout)
	if err != nil {
		if s := output.FirstNonEmptyLine(res.Stderr); s != "" {
			return nil, fmt.Errorf("%s", s)
		}
		return nil, err
	}
	return values, nil
}

func hdparmArgs(device string, wantCached, wantRead bool) []string {
	args := make([]string, 0, 3)
	if wantCached {
		args = append(args, "-T")
	}
	if wantRead {
		args = append(args, "-t")
	}
	return append(args, device)
}

// parseHdparm extracts the MB/sec rate from hdparm's timing lines: the
// "cached reads" line is `cached`, "buffered disk reads" is `read`. The rate is
// always the number after "=" and before "MB/sec" (the leading "N MB/GB in …" is
// the amount transferred, not the rate).
func parseHdparm(out string) (map[string]float64, error) {
	values := map[string]float64{}
	for _, line := range strings.Split(out, "\n") {
		eq := strings.LastIndex(line, "=")
		unit := strings.Index(line, "MB/sec")
		if eq < 0 || unit < 0 || unit < eq {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(line[eq+1:unit]), 64)
		if err != nil {
			continue
		}
		switch {
		case strings.Contains(line, "cached reads"):
			values[fieldCached] = v
		case strings.Contains(line, "buffered disk reads"):
			values[fieldRead] = v
		}
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("no timing in output")
	}
	return values, nil
}

// hdparmMessage renders the measured rates in a stable order.
func hdparmMessage(device string, values map[string]float64) string {
	parts := make([]string, 0, 2)
	for _, f := range []string{fieldRead, fieldCached} {
		if v, ok := values[f]; ok {
			parts = append(parts, fmt.Sprintf("%s=%.1f", f, v))
		}
	}
	return fmt.Sprintf("hdparm %s %s MB/s", device, strings.Join(parts, " "))
}
