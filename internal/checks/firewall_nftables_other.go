//go:build !linux

package checks

import (
	"context"
	"errors"

	"sermo/internal/execx"
)

// nftablesRuleCounter reads the loaded nftables rule count. Tests override it to
// avoid real netlink I/O.
var nftablesRuleCounter = countLoadedNftablesRules

// countLoadedNftablesRules reports that nftables rule counting is unavailable off
// Linux, where the netlink API it relies on does not exist. It still honors a
// cancelled context so callers observe the same cancellation signal as on Linux.
func countLoadedNftablesRules(ctx context.Context) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, execx.ContextError(err)
	}
	return 0, errors.New("nftables rule counting is only supported on Linux")
}
