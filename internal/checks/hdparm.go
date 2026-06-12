package checks

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"sermo/internal/execx"
)

// hdparmCheck times disk read throughput with hdparm and compares it to
// thresholds (a level check, like disk/load: OK==true means every predicate
// holds — i.e. the alerting condition is met). `read` is the buffered disk-read
// rate (hdparm -t, the real device speed) and `cached` is the cached-read rate
// (hdparm -T, memory/cache). Only the timings the configured predicates need are
// run, so a `cached`-only check skips the slow buffered pass. hdparm needs root
// and -t reads for ~3 s from the device, so schedule this on a long per-check
// `interval` (e.g. 24h) and give it a generous `timeout`. The measured MB/s are
// placed in Result.Data ("read"/"cached") for hooks and time-series recording.
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
	args := make([]string, 0, 3)
	if want["cached"] {
		args = append(args, "-T")
	}
	if want["read"] {
		args = append(args, "-t")
	}
	args = append(args, c.device)

	res, _ := c.runner.Run(ctx, "hdparm", args...)
	values, err := parseHdparm(res.Stdout)
	if err != nil {
		if s := firstLine(res.Stderr); s != "" {
			return c.result(false, "hdparm "+c.device+": "+s, start)
		}
		return c.result(false, "hdparm "+c.device+": "+err.Error(), start)
	}

	ok := levelPredsHold(c.preds, values)

	r := c.result(ok, hdparmMessage(c.device, values), start)
	r.Data = map[string]any{"device": c.device}
	for k, v := range values {
		r.Data[k] = v
	}
	return r
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
			values["cached"] = v
		case strings.Contains(line, "buffered disk reads"):
			values["read"] = v
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
	for _, f := range []string{"read", "cached"} {
		if v, ok := values[f]; ok {
			parts = append(parts, fmt.Sprintf("%s=%.1f", f, v))
		}
	}
	return fmt.Sprintf("hdparm %s %s MB/s", device, strings.Join(parts, " "))
}
