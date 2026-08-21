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

const (
	hdparmCommand = CheckTypeHdparm
	// hdparmRateSuffix is the tail hdparm's two rate units share; the byte scale
	// is the single character in front of it.
	hdparmRateSuffix = "B/sec"
	// hdparmKilobytesPerMegabyte mirrors hdparm's own MB->kB conversion, so a
	// kB/sec line reads back as the MB/s the predicates and the graph use.
	hdparmKilobytesPerMegabyte = 1024
)

// hdparmCheck compares configured hdparm timing rates with thresholds. It is
// condition-style: OK means every predicate holds. Only timings used by
// predicates are run; hdparm -t needs root and adds device I/O. hdparm reports
// a drive that fell off its bus as an unreadable timing rather than a named
// error, so a failed run asks sysfs whether the device still exists at all.
type hdparmCheck struct {
	base
	runner    execx.Runner
	device    string
	preds     []levelPred
	deviceBus BlockDeviceBusFunc
	probe     deviceProbe
	last      *lastSample
}

func (c *hdparmCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	start := run.start

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
		return c.failedProbe(prefix, msg, start)
	}
	values, err := parseHdparm(res.Stdout)
	if err != nil {
		msg := err.Error()
		if line := output.FirstNonEmptyLine(res.Stderr); line != "" {
			msg = line
		}
		return c.failedProbe(prefix, msg, start)
	}

	ok := levelPredsHold(c.preds, values)

	c.last.record("", values, start)
	r := c.result(ok, hdparmMessage(c.device, values), start)
	r.Data = withDeviceBus(HdparmResultData(c.device, values), c.deviceBus, c.device)
	return r
}

// failedProbe reports a timing hdparm could not take. It keeps the drive's
// identity and its last measured rates in the reading data, so a disk that stops
// answering still says which disk it was and how fast it last ran.
func (c *hdparmCheck) failedProbe(prefix, reason string, start time.Time) Result {
	r := c.deviceFailureResult(c.probe, prefix, c.device, reason, start)
	r.Data = c.last.into(r.Data, start)
	return r
}

// HdparmResultData is the persisted reading data for one hdparm throughput
// probe, shared by the check cycle and the snapshot-backed watch view.
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

// parseHdparm extracts the rate from hdparm's timing lines as MB/s: the
// "cached reads" line is `cached`, "buffered disk reads" is `read`. The rate is
// always the number after the last "=" (the leading "N MB/GB in …" is the
// amount transferred, not the rate).
func parseHdparm(out string) (map[string]float64, error) {
	values := map[string]float64{}
	for line := range strings.SplitSeq(out, checkLineSeparator) {
		eq := strings.LastIndex(line, "=")
		if eq < 0 {
			continue
		}
		v, ok := parseHdparmRate(line[eq+1:])
		if !ok {
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

// parseHdparmRate reads one timing line's rate tail — "  113.07 MB/sec" — as
// MB/s. hdparm drops to kB/sec below 1 MB/s, which is exactly what a disk
// answering from standby reports because its whole timing window went on
// spinning up: a slow sample, not the absent one "no timing in output" claims.
func parseHdparmRate(tail string) (float64, bool) {
	unit := strings.Index(tail, hdparmRateSuffix)
	if unit < 1 {
		return 0, false
	}
	var scale float64
	switch tail[unit-1] {
	case 'M':
		scale = 1
	case 'k':
		scale = 1.0 / hdparmKilobytesPerMegabyte
	default:
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(tail[:unit-1]), numericBits64)
	if err != nil {
		return 0, false
	}
	return v * scale, true
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
