package config

import (
	"context"
	"strings"
	"time"

	"sermo/internal/servicemgr"
	"sermo/internal/strutil"
)

const serviceUnitDiscoveryTimeout = 2 * time.Second

func cloneServiceUnits(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for backend, units := range in {
		out[backend] = append([]string(nil), units...)
	}
	return out
}

func (c *Config) activeServiceUnits(ctx context.Context, backend string) []string {
	if c == nil || backend == "" || backend == string(servicemgr.BackendAuto) {
		return nil
	}
	if c.serviceUnits == nil {
		c.serviceUnits = map[string][]string{}
	}
	if units, ok := c.serviceUnits[backend]; ok {
		return units
	}
	units, err := servicemgr.ListActiveUnits(ctx, servicemgr.Backend(backend), nil, serviceUnitDiscoveryTimeout)
	if err != nil {
		c.serviceUnits[backend] = nil
		return nil
	}
	c.serviceUnits[backend] = normalizeServiceUnits(units)
	return c.serviceUnits[backend]
}

func normalizeServiceUnits(units []string) []string {
	trimmed := make([]string, len(units))
	for i, unit := range units {
		trimmed[i] = strings.TrimSpace(unit)
	}
	return strutil.Unique(trimmed)
}
