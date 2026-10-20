// Package cliutil holds small command-line plumbing shared by the sermod and
// sermoctl entry points.
package cliutil

import (
	"fmt"
	"path/filepath"
	"strings"
)

const pflagUnknownFlagPrefix = "unknown flag: "

// NormalizePflagError rewrites pflag's "unknown flag: --x" parse error into the
// project's "unknown flag --x" wording; any other error is returned unchanged.
func NormalizePflagError(err error) error {
	if msg := err.Error(); strings.HasPrefix(msg, pflagUnknownFlagPrefix) {
		return fmt.Errorf("unknown flag %s", strings.TrimPrefix(msg, pflagUnknownFlagPrefix))
	}
	return err
}

// AbsPath resolves an operator-supplied path flag against the current working
// directory and cleans it, so everything downstream reads one absolute, clean
// path instead of resolving it again relative to wherever it runs.
func AbsPath(flag, value string) (string, error) {
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("--%s %q: %w", flag, value, err)
	}
	return abs, nil
}
