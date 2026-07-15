package app

import (
	"context"
	"slices"
	"sync"
	"time"

	"sermo/internal/web"
)

const (
	// catalogInventoryCacheTTL keeps web software inventories from running
	// version and health commands on ordinary dashboard refreshes.
	catalogInventoryCacheTTL = 5 * time.Minute
)

type catalogInventoryCache struct {
	mu      sync.Mutex
	at      time.Time
	items   []web.CatalogItem
	refresh chan struct{} // non-nil while a scan is rebuilding the cache; closed when it finishes
	list    func(context.Context) []web.CatalogItem
}

func (b *WebBackend) catalogItems(
	ctx context.Context,
	inventory *catalogInventoryCache,
	load func(context.Context) []web.CatalogItem,
) []web.CatalogItem {
	// The inventory scan runs version/binary probes and can take seconds, so it
	// must not run under the cache mutex: only one request rebuilds the cache
	// while every other viewer is served the previous
	// inventory — or, on a cold start, waits for that first scan.
	for {
		inventory.mu.Lock()
		cached := slices.Clone(inventory.items)
		observedAt := inventory.at
		hasCache := !observedAt.IsZero()
		if hasCache && time.Since(observedAt) < catalogInventoryCacheTTL {
			inventory.mu.Unlock()
			return decorateCatalogItems(cached, observedAt)
		}
		refresh := inventory.refresh
		if refresh == nil {
			break // become the rebuilding request; lock still held
		}
		inventory.mu.Unlock()
		if hasCache {
			// An expired-but-complete inventory beats queueing every viewer
			// behind the scan that is already refreshing it.
			return decorateCatalogItems(cached, observedAt)
		}
		select {
		case <-refresh:
			// Re-check the cache the finished scan produced.
		case <-ctx.Done():
			return nil
		}
	}
	done := make(chan struct{})
	inventory.refresh = done
	inventory.mu.Unlock()
	// Clear the in-flight marker in a defer so even a panicking scan cannot
	// leave cold-start viewers waiting on the channel forever. The deferred
	// close runs after the cache update below, so woken viewers always
	// re-check an already-updated cache.
	defer func() {
		inventory.mu.Lock()
		inventory.refresh = nil
		inventory.mu.Unlock()
		close(done)
	}()

	items := load(ctx)

	inventory.mu.Lock()
	if ctx.Err() != nil {
		// A cancelled request yields a partial inventory; caching it would
		// serve an incomplete list to every viewer for the full TTL.
		// Prefer the previous complete cache when there is one.
		if !inventory.at.IsZero() {
			items = slices.Clone(inventory.items)
		}
		observedAt := inventory.at
		inventory.mu.Unlock()
		return decorateCatalogItems(items, observedAt)
	}
	inventory.at = time.Now()
	observedAt := inventory.at
	inventory.items = slices.Clone(items)
	inventory.mu.Unlock()
	return decorateCatalogItems(items, observedAt)
}
