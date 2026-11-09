//go:build linux

package checks

import (
	"context"
	"fmt"

	"sermo/internal/execx"

	"github.com/google/nftables"
	"github.com/mdlayher/netlink"
)

// nftablesRuleCounter reads the loaded nftables rule count. Tests override it to
// avoid real netlink I/O.
var nftablesRuleCounter = countLoadedNftablesRules

// countLoadedNftablesRules walks the rules synchronously. Cancellation closes
// the socket, so no detached walk survives into the next monitoring cycle.
func countLoadedNftablesRules(ctx context.Context) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("count rules: %w", execx.ContextError(err))
	}
	var interrupt func()
	conn, err := nftables.New(nftables.AsLasting(), nftables.WithSockOptions(func(socket *netlink.Conn) error {
		// Close the underlying socket, not CloseLasting: the latter clears
		// nftables' socket pointer and lets a racing query open a fresh one.
		interrupt = func() { _ = socket.Close() }
		return nil
	}))
	if err != nil {
		return 0, fmt.Errorf("open nftables: %w", err)
	}
	defer func() { _ = conn.CloseLasting() }()
	return listNftablesRules(ctx, conn, interrupt)
}

type nftablesRuleLister interface {
	ListChains() ([]*nftables.Chain, error)
	GetRules(table *nftables.Table, chain *nftables.Chain) ([]*nftables.Rule, error)
}

func listNftablesRules(ctx context.Context, conn nftablesRuleLister, interrupt func()) (uint64, error) {
	stop := context.AfterFunc(ctx, interrupt)
	defer stop()
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("count rules: %w", execx.ContextError(err))
	}
	chains, err := conn.ListChains()
	if ctx.Err() != nil {
		return 0, fmt.Errorf("count rules: %w", execx.ContextError(ctx.Err()))
	}
	if err != nil {
		return 0, fmt.Errorf("list nftables chains: %w", err)
	}
	var total uint64
	for _, chain := range chains {
		if err := ctx.Err(); err != nil {
			return 0, fmt.Errorf("count rules: %w", execx.ContextError(err))
		}
		if chain.Table == nil {
			continue
		}
		rules, err := conn.GetRules(chain.Table, chain)
		if ctx.Err() != nil {
			return 0, fmt.Errorf("count rules: %w", execx.ContextError(ctx.Err()))
		}
		if err != nil {
			return total, fmt.Errorf("chain %s/%s: %w", chain.Table.Name, chain.Name, err)
		}
		total += uint64(len(rules))
	}
	return total, nil
}
