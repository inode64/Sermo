package config

import (
	"testing"

	"sermo/internal/checks"
)

// Every built-in type must have exactly one field validator. Without this
// parity check, adding a builder can make malformed YAML look valid because the
// generic validator recognizes the type and silently skips its fields.
func TestSingleShotCheckValidatorsMatchRegistry(t *testing.T) {
	registered := make(map[string]struct{}, len(checks.TypeInfos()))
	for _, info := range checks.TypeInfos() {
		registered[info.Name] = struct{}{}
		validate, ok := singleShotCheckValidators[info.Name]
		if !ok || validate == nil {
			t.Errorf("built-in check type %q has no field validator", info.Name)
		}
	}
	for checkType, validate := range singleShotCheckValidators {
		if _, ok := registered[checkType]; !ok {
			t.Errorf("field validator references unknown built-in check type %q", checkType)
		}
		if validate == nil {
			t.Errorf("field validator for %q is nil", checkType)
		}
	}
}
