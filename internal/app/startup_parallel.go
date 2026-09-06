package app

import "sync"

// startupParallelism bounds how many services are wired at once while the
// daemon builds its workers and its web backend. Each service costs a handful
// of init-backend queries (systemctl show, cgroup listings) and /proc scans;
// wired one after another, 49 services took a loaded VM host ten minutes to
// reach the web listener, and the fleet updater rolled the release back. The
// engine's check parallelism already says how many external probes the host
// should carry at once, so startup borrows it.
func startupParallelism(maxParallel int) int {
	if maxParallel < 1 {
		return 1
	}
	return maxParallel
}

// forEachParallel runs fn for every index below n with at most limit calls in
// flight and returns once all of them finished. Callers keep results per index
// so the assembled order stays the sorted, deterministic one.
func forEachParallel(n, limit int, fn func(i int)) {
	sem := make(chan struct{}, startupParallelism(limit))
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
