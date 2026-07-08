// Package units names shared numeric conversion factors.
package units

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
