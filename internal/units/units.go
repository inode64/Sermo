// Package units names shared numeric conversion factors and the compact
// duration rendering built on them.
package units

import (
	"fmt"
	"strings"
	"time"
)

const (
	// BytesPerKiB converts kibibytes to bytes.
	BytesPerKiB = 1024
	// KiBPerMiB converts mebibytes expressed as kibibytes to MiB counts.
	KiBPerMiB = 1024
	// BytesPerMiB converts mebibytes to bytes.
	BytesPerMiB = BytesPerKiB * KiBPerMiB
	// BytesPerGiB converts gibibytes to bytes.
	BytesPerGiB = BytesPerMiB * KiBPerMiB
	// BytesPerTiB converts tebibytes to bytes.
	BytesPerTiB = BytesPerGiB * KiBPerMiB
)

const (
	// SecondsPerMinute converts minutes to seconds.
	SecondsPerMinute = 60
	// MinutesPerHour converts hours to minutes.
	MinutesPerHour = 60
	// HoursPerDay converts days to hours.
	HoursPerDay = 24
	// DaysPerWeek converts weeks to days.
	DaysPerWeek = 7
	// DaysPerMonthApprox is the 30-day month approximation used for compact display windows.
	DaysPerMonthApprox = 30
)

// HumanizeDuration renders a duration compactly: whole units chain
// greatest-first ("1h30m", "1d6h"), extending Go's units upward with day (d),
// week (w) and month (mo, taken as 30 days). A zero or negative duration is
// "0s" — the only case where a 0 component survives — and sub-second durations
// keep the standard library formatting (e.g. "1.5s").
func HumanizeDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d%time.Second != 0 {
		return d.String()
	}
	total := int64(d / time.Second)
	const (
		minute = SecondsPerMinute
		hour   = MinutesPerHour * minute
		day    = HoursPerDay * hour
		week   = DaysPerWeek * day
		month  = DaysPerMonthApprox * day // display approximation
	)
	durationUnits := []struct {
		secs   int64
		suffix string
	}{
		{month, "mo"},
		{week, "w"},
		{day, "d"},
		{hour, "h"},
		{minute, "m"},
		{1, "s"},
	}
	var b strings.Builder
	for _, u := range durationUnits {
		if total >= u.secs {
			fmt.Fprintf(&b, "%d%s", total/u.secs, u.suffix)
			total %= u.secs
		}
	}
	return b.String()
}
