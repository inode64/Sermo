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
	// DaysPerMonthApprox is the 30-day month approximation used for compact display windows.
	DaysPerMonthApprox = 30
	// MonthsPerYear converts display years to display months.
	MonthsPerYear = 12
	// DaysPerYearApprox is the display-year length: exactly 12 30-day months
	// (360 days), so month components can never overflow into a 13th month.
	DaysPerYearApprox = MonthsPerYear * DaysPerMonthApprox
)

// Duration display promotes to a larger head unit only well past that unit's
// boundary, so "1d 0h" never appears right at 24h. Each ceiling is the largest
// (inclusive) value still shown with that head unit. Keep these in step with
// fmtDuration in internal/web/src/format.js — both sides must emit identical
// strings.
const (
	// DurationSecondsCeiling is the largest duration shown as bare seconds.
	DurationSecondsCeiling = 360
	// DurationHoursCeiling is the largest hour count shown before promoting to days.
	DurationHoursCeiling = 72
	// DurationDaysCeiling is the largest day count shown before promoting to months.
	DurationDaysCeiling = 120
	// DurationMonthsCeiling is the largest month count shown before promoting to years.
	DurationMonthsCeiling = 24
)

// HumanizeDuration renders a duration as space-separated whole components,
// greatest-first, skipping zero components ("2h 3m 20s", "3y 2mo 10d 12h 20s").
// The head unit is promoted with hysteresis: bare seconds up to 360s, hours up
// to 72h ("70h 15m"), days up to 120d ("100d 6h"), months (30 days) up to 24mo
// ("20mo 12d"), then years (12 months). A zero or negative duration is "0s" —
// the only case where a 0 component survives — and sub-second durations keep
// the standard library formatting (e.g. "1.5s").
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
		month  = DaysPerMonthApprox * day // display approximation
		year   = MonthsPerYear * month
	)
	if total <= DurationSecondsCeiling {
		return fmt.Sprintf("%ds", total)
	}
	durationUnits := []struct {
		secs   int64
		suffix string
	}{
		{year, "y"},
		{month, "mo"},
		{day, "d"},
		{hour, "h"},
		{minute, "m"},
		{1, "s"},
	}
	// Pick the head unit band: a unit leads only once the value exceeds the
	// lower unit's ceiling (e.g. days lead only past 72 hours).
	var head int
	switch {
	case total > DurationMonthsCeiling*month:
		head = 0 // years
	case total > DurationDaysCeiling*day:
		head = 1 // months
	case total > DurationHoursCeiling*hour:
		head = 2 // days
	default:
		head = 3 // hours (or minutes/seconds when the larger components are 0)
	}
	var b strings.Builder
	for _, u := range durationUnits[head:] {
		if total >= u.secs {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			fmt.Fprintf(&b, "%d%s", total/u.secs, u.suffix)
			total %= u.secs
		}
	}
	return b.String()
}
