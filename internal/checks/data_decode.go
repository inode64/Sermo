package checks

import "encoding/json"

// DecodeDataSlice reads a typed slice a check published in Result.Data: the
// live []T, or the []any of maps a persisted snapshot holds after JSON
// hydration. The owning struct's JSON tags stay the one schema, instead of a
// field-by-field parser per reader. A value that does not decode yields nil.
func DecodeDataSlice[T any](value any) []T {
	if typed, ok := value.([]T); ok {
		return typed
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var out []T
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}
