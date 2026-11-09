//go:build linux

package checks

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/nftables"
)

type fakeNftablesRules struct {
	chains func() ([]*nftables.Chain, error)
	rules  func(*nftables.Chain) ([]*nftables.Rule, error)
}

func (f fakeNftablesRules) ListChains() ([]*nftables.Chain, error) { return f.chains() }
func (f fakeNftablesRules) GetRules(_ *nftables.Table, chain *nftables.Chain) ([]*nftables.Rule, error) {
	return f.rules(chain)
}

func TestNftablesRuleWalk(t *testing.T) {
	for _, fail := range []string{"", "list", "rules"} {
		t.Run("failure="+fail, func(t *testing.T) {
			table := &nftables.Table{Name: "filter"}
			calls := 0
			failure := errors.New("netlink failed")
			conn := fakeNftablesRules{
				chains: func() ([]*nftables.Chain, error) {
					if fail == "list" {
						return nil, failure
					}
					return []*nftables.Chain{{}, {Table: table, Name: "input"}, {Table: table, Name: "output"}}, nil
				},
				rules: func(*nftables.Chain) ([]*nftables.Rule, error) {
					calls++
					if fail == "rules" {
						return nil, failure
					}
					return []*nftables.Rule{{}, {}}, nil
				},
			}
			n, err := listNftablesRules(t.Context(), conn, func() { t.Error("completed walk interrupted") })
			if fail == "" && (err != nil || n != 4 || calls != 2) {
				t.Fatalf("walk = %d, %v, calls=%d", n, err, calls)
			}
			if fail != "" && !errors.Is(err, failure) {
				t.Fatalf("walk error = %v, want %v", err, failure)
			}
		})
	}
}

func TestNftablesCancellationStopsWalk(t *testing.T) {
	for _, where := range []string{"before list", "during list", "during rules", "between chains"} {
		t.Run(where, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			closed := make(chan struct{})
			waitForClose := func() {
				select {
				case <-closed:
				case <-time.After(time.Second):
					t.Fatal("cancellation did not close the netlink socket")
				}
			}
			table := &nftables.Table{Name: "filter"}
			listed, queried := 0, 0
			conn := fakeNftablesRules{
				chains: func() ([]*nftables.Chain, error) {
					listed++
					if where == "during list" {
						cancel()
						waitForClose()
						return nil, net.ErrClosed
					}
					return []*nftables.Chain{{Table: table}, {Table: table}}, nil
				},
				rules: func(*nftables.Chain) ([]*nftables.Rule, error) {
					queried++
					cancel()
					if where == "during rules" {
						waitForClose()
						return nil, net.ErrClosed
					}
					return []*nftables.Rule{{}}, nil
				},
			}
			if where == "before list" {
				cancel()
			}
			n, err := listNftablesRules(ctx, conn, func() { close(closed) })
			if n != 0 || err == nil || !strings.Contains(err.Error(), "cancel") {
				t.Fatalf("walk = %d, %v; want cancellation without a partial count", n, err)
			}
			if where == "before list" && listed != 0 || where == "during list" && queried != 0 || queried > 1 {
				t.Fatalf("queries after cancellation: list=%d, rules=%d", listed, queried)
			}
		})
	}
}
