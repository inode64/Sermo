package metrics

import (
	"fmt"
	"sync"
	"time"
)

// cachedSample coalesces successful host observations. Each caller supplies its
// own freshness bound; zero forces a read. A failed refresh returns the failure,
// never the prior sample. Partial samples are returned but not retained.
type cachedSample[T any] struct {
	mu    sync.Mutex
	value T
	at    time.Time
}

func (c *cachedSample[T]) readInto(now time.Time, maxAge time.Duration, read func() (T, bool, error), out *T) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	age := now.Sub(c.at)
	if !c.at.IsZero() && age >= 0 && age < maxAge {
		*out = c.value
		return nil
	}
	value, complete, err := read()
	*out = value
	if err != nil || !complete {
		c.at = time.Time{}
		if err != nil {
			return fmt.Errorf("sample host metrics: %w", err)
		}
		return nil
	}
	c.value, c.at = value, now
	return nil
}
