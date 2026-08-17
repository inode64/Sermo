package checks

import (
	"strconv"

	"sermo/internal/metrics"
)

const (
	checkLineSeparator = "\n"
	numericBaseDecimal = 10
	numericBits64      = 64
	floatPrecisionAuto = -1
	floatFormatFixed   = 'f'
	percentScale       = metrics.PercentScale
)

// unnamedProcess stands in for a process a check found but could not name: its
// comm or exe could not be read, usually because it exited between the /proc walk
// and the name read. Shared by the checks that enumerate processes they did not
// select (inotify holders, strays), so an operator sees one spelling.
const unnamedProcess = "unknown"

// formatThreshold renders a configured numeric bound the way the operator wrote
// it — `5`, not `5.000000` — for the messages that quote it back. Thresholds are
// parsed as float64 whatever their YAML form, so every check that names one in its
// message needs this and none of them wants the thousands grouping
// FormatDisplayValue applies to sampled readings.
func formatThreshold(v float64) string {
	return strconv.FormatFloat(v, floatFormatFixed, floatPrecisionAuto, numericBits64)
}
