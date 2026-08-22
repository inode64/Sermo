package checks

import "slices"

// Severity names. One vocabulary serves both the per-check `severity:`
// declaration and the grades an output-analysis rule assigns, so an operator
// writes the same word wherever Sermo asks how grave a finding is.
const (
	// SeverityError is the default: a failing check is an outage to act on. It
	// reads red, logs at error level and counts against health.
	SeverityError = "error"
	// SeverityWarning demotes a failing check to an advisory. It still evaluates
	// its window and still runs its actions, but it reads amber rather than red,
	// logs at warn level, and stays out of aggregated health and the SLA — for a
	// measurement whose bad value is worth seeing and not worth waking anyone.
	SeverityWarning = "warning"
	// SeverityOK grades an analysis match as benign. Only output analysis uses
	// it: a check itself is either an error or a warning.
	SeverityOK = "ok"
)

// checkSeverities is the immutable package-owned catalog of values `severity:`
// accepts on a check. Consumers receive a copy through CheckSeverities so they
// cannot alter validation at runtime. SeverityOK is deliberately absent: a check
// with nothing to say does not fail in the first place.
var checkSeverities = [...]string{SeverityError, SeverityWarning}

// CheckSeveritySummary names the accepted per-check severities for error text.
const CheckSeveritySummary = SeverityError + " or " + SeverityWarning

// CheckSeverities returns the values a check's `severity:` accepts, in display
// order.
func CheckSeverities() []string { return slices.Clone(checkSeverities[:]) }

// IsCheckSeverity reports whether s names a severity a check may declare.
func IsCheckSeverity(s string) bool { return slices.Contains(checkSeverities[:], s) }

// ResolveSeverity layers one declaration over its fallback: an explicit value
// wins, an empty or unusable one inherits, and a chain that declares nothing is
// an error. A watch stacks metric over check over watch through it, so the
// narrowest declaration is the one that decides.
func ResolveSeverity(declared, fallback string) string {
	if IsCheckSeverity(declared) {
		return declared
	}
	if IsCheckSeverity(fallback) {
		return fallback
	}
	return SeverityError
}

// IsWarning reports whether a severity demotes a failure to an advisory. The
// empty severity is an error, so an unconfigured check keeps today's behavior.
func IsWarning(severity string) bool { return severity == SeverityWarning }
