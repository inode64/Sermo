package config

import (
	"context"
	"strings"
	"time"

	"sermo/internal/servicemgr"
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
	seen := map[string]struct{}{}
	out := make([]string, 0, len(units))
	for _, unit := range units {
		out = appendServiceUnit(out, seen, unit)
	}
	return out
}

func appendServiceUnit(out []string, seen map[string]struct{}, unit string) []string {
	unit = strings.TrimSpace(unit)
	if unit == "" {
		return out
	}
	if _, ok := seen[unit]; ok {
		return out
	}
	seen[unit] = struct{}{}
	return append(out, unit)
}
