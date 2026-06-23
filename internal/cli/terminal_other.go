//go:build !linux

package cli

import "io"

func stdinIsTerminal(io.Reader) bool {
	return false
}
