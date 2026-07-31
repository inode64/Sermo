package config

import (
	"fmt"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
)

// FileWatchPaths returns the paths selected by a file watch.
func FileWatchPaths(check map[string]any) ([]string, error) {
	paths, hasPaths := check[checks.CheckKeyPaths]
	if !hasPaths {
		return nil, fmt.Errorf("file check %s is required", checks.CheckKeyPaths)
	}
	switch paths.(type) {
	case []any, []string:
	default:
		return nil, fmt.Errorf("file check %s must be a non-empty list of strings", checks.CheckKeyPaths)
	}
	selected, err := cfgval.StrictStringArray(paths)
	if err != nil || len(selected) == 0 {
		return nil, fmt.Errorf("file check %s must be a non-empty list of strings", checks.CheckKeyPaths)
	}
	seen := make(map[string]struct{}, len(selected))
	for _, selectedPath := range selected {
		if selectedPath == "" {
			return nil, fmt.Errorf("file check %s must be a non-empty list of strings", checks.CheckKeyPaths)
		}
		if _, duplicate := seen[selectedPath]; duplicate {
			return nil, fmt.Errorf("file check %s must not contain duplicate path %q", checks.CheckKeyPaths, selectedPath)
		}
		seen[selectedPath] = struct{}{}
	}
	return selected, nil
}
