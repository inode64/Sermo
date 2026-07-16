package conn

import "context"

// probeWithDeadline returns promptly when ctx expires even when a third-party
// client ignores context during its handshake or RPC. The buffered result
// channel lets that client return later without blocking its goroutine.
func probeWithDeadline(ctx context.Context, probe func(context.Context) (Result, error)) (Result, error) {
	type probeResult struct {
		result Result
		err    error
	}
	results := make(chan probeResult, 1)
	go func() {
		result, err := probe(ctx)
		results <- probeResult{result: result, err: err}
	}()
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case result := <-results:
		return result.result, result.err
	}
}
