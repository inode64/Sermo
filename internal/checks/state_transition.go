package checks

import "fmt"

// stateTransitionSpec describes the common state-watch behavior: an expected
// value fires while it matches; otherwise the first value establishes a
// baseline and later changes fire once while preserving the old/new reading.
type stateTransitionSpec struct {
	target        string
	current       string
	expected      string
	expectedLabel string
	data          map[string]any
	primed        *bool
	previous      *string
}

func evaluateStateTransition(spec stateTransitionSpec) (bool, string) {
	if spec.expected != "" {
		spec.data[DataKeyValue] = spec.current
		label := spec.target
		if spec.expectedLabel != "" {
			label += " " + spec.expectedLabel
		}
		return spec.current == spec.expected, fmt.Sprintf("%s %s (want %s)", label, spec.current, spec.expected)
	}
	if !*spec.primed {
		*spec.primed, *spec.previous = true, spec.current
		return false, fmt.Sprintf("%s state baseline %s", spec.target, spec.current)
	}
	previous := *spec.previous
	changed := spec.current != previous
	spec.data[DataKeyOld], spec.data[DataKeyNew], spec.data[DataKeyValue] = previous, spec.current, spec.current
	*spec.previous = spec.current
	return changed, fmt.Sprintf("%s state %s->%s", spec.target, previous, spec.current)
}
