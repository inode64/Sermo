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

const hdparmCommand = CheckTypeHdparm

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
	prefix := hdparmCommand + " " + c.device
	res, runErr := c.runner.Run(ctx, hdparmCommand, hdparmArgs(c.device, want[fieldCached], want[fieldRead])...)
	if res.ExitCode == execx.ExitCodeRunFailure {
		msg := execx.OperatorFailureOr(runErr, res, c.timeout, execx.CommandDidNotStart)
		return c.result(false, prefix+": "+msg, start)
	}
	values, err := parseHdparm(res.Stdout)
	if err != nil {
		if s := output.FirstNonEmptyLine(res.Stderr); s != "" {
			return c.result(false, prefix+": "+s, start)
		}
		return c.result(false, prefix+": "+err.Error(), start)
	}

	ok := levelPredsHold(c.preds, values)

	r := c.result(ok, hdparmMessage(c.device, values), start)
	r.Data = HdparmResultData(c.device, values)
	return r
}

// HdparmResultData is the persisted reading data for one hdparm throughput
// probe, shared by the check cycle and the live watch view.
func HdparmResultData(device string, values map[string]float64) map[string]any {
	data := map[string]any{DataKeyDevice: device}
	for k, v := range values {
		data[k] = v
	}
	return data
}

func hdparmArgs(device string, wantCached, wantRead bool) []string {
	const hdparmArgumentCapacity = 3

	args := make([]string, 0, hdparmArgumentCapacity)
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
	for line := range strings.SplitSeq(out, checkLineSeparator) {
		eq := strings.LastIndex(line, "=")
		unit := strings.Index(line, "MB/sec")
		if eq < 0 || unit < 0 || unit < eq {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(line[eq+1:unit]), numericBits64)
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
		return nil, errors.New("no timing in output")
	}
	return values, nil
}

// hdparmMessage renders the measured rates in a stable order.
func hdparmMessage(device string, values map[string]float64) string {
	const hdparmMeasuredFieldCapacity = 2

	parts := make([]string, 0, hdparmMeasuredFieldCapacity)
	for _, f := range []string{fieldRead, fieldCached} {
		if v, ok := values[f]; ok {
			parts = append(parts, fmt.Sprintf("%s=%.1f", f, v))
		}
	}
	return fmt.Sprintf("hdparm %s %s MB/s", device, strings.Join(parts, " "))
}
