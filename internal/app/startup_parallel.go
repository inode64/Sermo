package app

import "sync"

// ForEachParallel runs fn for every index below n with at most limit calls in
// flight (at least one) and returns once all of them finished. Callers keep
// results per index so the assembled order stays the sorted, deterministic one.
// The daemon wires its services through it at startup: each costs a handful of
// init-backend queries and /proc scans, and wired one after another 49 services
// once took a loaded VM host ten minutes to reach the web listener, so it
// borrows the engine's check parallelism. sermoctl uses it for the same
// per-service queries when sermod does not answer.
func ForEachParallel(n, limit int, fn func(i int)) {
	sem := make(chan struct{}, max(limit, 1))
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			fn(i)
		}(i)
	}
	wg.Wait()
}
