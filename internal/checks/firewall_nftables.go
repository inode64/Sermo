package checks

import (
	"context"
	"errors"
	"fmt"

	"sermo/internal/execx"

	"github.com/google/nftables"
)

// nftablesRuleCounter reads the loaded nftables rule count. Tests override it to
// avoid real netlink I/O.
var nftablesRuleCounter = countLoadedNftablesRules

// countLoadedNftablesRules returns how many nftables rules are loaded. The
// caller's context bounds the netlink walk; cancellation stops between chains.
func countLoadedNftablesRules(ctx context.Context) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, nftablesContextError(err)
	}
	type out struct {
		n   uint64
		err error
	}
	ch := make(chan out, 1)
	go func() {
		n, err := listNftablesRules()
		ch <- out{n: n, err: err}
	}()
	select {
	case <-ctx.Done():
		return 0, nftablesContextError(ctx.Err())
	case r := <-ch:
		return r.n, r.err
	}
}

func nftablesContextError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(execx.ContextFailure(err, execx.NoTimeout))
}

func listNftablesRules() (uint64, error) {
	conn, err := nftables.New(nftables.AsLasting())
	if err != nil {
		return 0, err
	}
	defer func() { _ = conn.CloseLasting() }()

	chains, err := conn.ListChains()
	if err != nil {
		return 0, err
	}
	var total uint64
	for _, chain := range chains {
		if chain == nil || chain.Table == nil {
			continue
		}
		rules, err := conn.GetRules(chain.Table, chain)
		if err != nil {
			return total, fmt.Errorf("chain %s/%s: %w", chain.Table.Name, chain.Name, err)
		}
		total += uint64(len(rules))
	}
	return total, nil
}
