package conn

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"sermo/internal/dockerctl"
)

// ParamKeyDockerMemory opts into a single Docker API memory sample.
const ParamKeyDockerMemory = "memory"

const (
	dockerMemoryUsage   = "memory.used_bytes"
	dockerMemoryLimit   = "memory.limit_bytes"
	dockerMemoryLimited = "memory.limited"
	dockerMemoryUsedPct = "memory.used_pct"
	dockerPercentScale  = 100
)

func dockerContainerMemory(ctx context.Context, client *dockerctl.Client, container dockerctl.Container, result *Result) error {
	if container.ID == "" || !container.State.Running || container.HostConfig.Memory == nil {
		return errors.New("docker memory requires an identified running container and its configured limit")
	}
	stats, err := client.Stats(ctx, container.ID)
	if err != nil {
		return fmt.Errorf("read Docker memory: %w", err)
	}
	if stats.ID != container.ID || stats.MemoryStats.Usage == nil || stats.MemoryStats.Limit == nil {
		return errors.New("docker memory sample has missing data or a different container ID")
	}
	if *container.HostConfig.Memory < 0 {
		return errors.New("docker memory limit is invalid")
	}
	limited := *container.HostConfig.Memory > 0
	limit := uint64(0)
	percent := float64(0)
	if limited {
		limit = *stats.MemoryStats.Limit
		if limit == 0 {
			return errors.New("docker memory limit is zero for a limited container")
		}
		percent = float64(*stats.MemoryStats.Usage) * dockerPercentScale / float64(limit)
	}
	result.Extra[dockerMemoryUsage] = strconv.FormatUint(*stats.MemoryStats.Usage, numericBaseDecimal)
	result.Extra[dockerMemoryLimit] = strconv.FormatUint(limit, numericBaseDecimal)
	result.Extra[dockerMemoryLimited] = strconv.FormatBool(limited)
	result.Extra[dockerMemoryUsedPct] = strconv.FormatFloat(percent, 'f', -1, 64)
	return nil
}
