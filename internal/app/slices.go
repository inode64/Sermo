package app

import "time"

// mapSlice converts a slice element-by-element via conv, preserving the
// nil-in/nil-out convention the state snapshot converters rely on.
func mapSlice[S, D any](in []S, conv func(S) D) []D {
	if len(in) == 0 {
		return nil
	}
	out := make([]D, 0, len(in))
	for _, v := range in {
		out = append(out, conv(v))
	}
	return out
}

// filterSince returns the samples whose timestamp (via at) is not before
// cutoff; the rolling-window trim shared by the runtime metric recorders.
func filterSince[T any](samples []T, cutoff time.Time, at func(T) time.Time) []T {
	out := make([]T, 0, len(samples))
	for _, s := range samples {
		if !at(s).Before(cutoff) {
			out = append(out, s)
		}
	}
	return out
}
