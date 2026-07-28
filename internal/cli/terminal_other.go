//go:build !linux

package cli

import (
	"errors"
	"io"
)

func stdinIsTerminal(io.Reader) bool {
	return false
}

// disableEcho has no non-Linux implementation; stdinIsTerminal already reports
// false there, so the interactive prompt is never reached.
func disableEcho(io.Reader) (func(), error) {
	return nil, errors.New("terminal echo control is not available on this platform")
}
