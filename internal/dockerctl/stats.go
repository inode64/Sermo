package dockerctl

import "context"

// ContainerStats retains explicit zero readings and detects missing memory data.
type ContainerStats struct {
	ID          string `json:"id"`
	MemoryStats struct {
		Usage *uint64 `json:"usage"`
		Limit *uint64 `json:"limit"`
	} `json:"memory_stats"`
}

// Stats reads a single sample for an inspected ID; names may be reused while
// containers are recreated, so callers must pin the ID before requesting stats.
func (c *Client) Stats(ctx context.Context, id string) (ContainerStats, error) {
	var stats ContainerStats
	if err := c.get(ctx, containerPath(id, "/stats")+"?stream=false&one-shot=true", dockerResponseBodyLimit, &stats); err != nil {
		return ContainerStats{}, err
	}
	return stats, nil
}
