package servicemgr

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"sermo/internal/execx"
)

func runSystemctlShow(ctx context.Context, runner execx.Runner, timeout time.Duration, property, unit string) (execx.Result, error) {
	res, err := execx.Run(ctx, runner, timeout, cmdSystemctl, systemctlCmdShow, systemctlFlagProperty, property, systemctlFlagValue, commandArgTerminator, unit)
	if err != nil {
		return res, fmt.Errorf("systemctl show %s for %s: %w", property, unit, err)
	}
	return res, nil
}

// systemdTimespanUnits are the suffixes systemctl prints for a timespan
// property, up to hours; a longer span reads as unbounded.
var systemdTimespanUnits = map[string]time.Duration{
	"us":  time.Microsecond,
	"ms":  time.Millisecond,
	"s":   time.Second,
	"min": time.Minute,
	"h":   time.Hour,
}

// parseSystemdTimespan parses a timespan as systemctl prints it ("1min 30s",
// "2.5s"). infinity, 0 and anything unrecognized report no bound.
func parseSystemdTimespan(s string) (time.Duration, bool) {
	var total time.Duration
	for part := range strings.FieldsSeq(s) {
		split := strings.IndexFunc(part, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
		if split <= 0 {
			return 0, false
		}
		unit, known := systemdTimespanUnits[part[split:]]
		value, err := strconv.ParseFloat(part[:split], 64)
		if !known || err != nil {
			return 0, false
		}
		total += time.Duration(value * float64(unit))
	}
	return total, total > 0
}
