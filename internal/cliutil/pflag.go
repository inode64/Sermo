// Package cliutil holds small command-line plumbing shared by the sermod and
// sermoctl entry points.
package cliutil

import (
	"fmt"
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
