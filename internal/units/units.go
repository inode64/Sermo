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
