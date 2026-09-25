package app

import (
	"fmt"
	"maps"
	"slices"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
)

// serviceCheckCatalog shares resolved check cadence between workers and the web
// view. Explicit intervals are rounded once; defaults run every cycle.
type serviceCheckCatalog struct {
	names     []string
	types     map[string]string
	intervals map[string]time.Duration
	cycles    map[string]int
	warnings  []string
}

func checkCatalog(tree map[string]any, resolution time.Duration) serviceCheckCatalog {
	section, ok := tree[config.SectionChecks].(map[string]any)
	if !ok {
		return serviceCheckCatalog{}
	}
	catalog := serviceCheckCatalog{
		names:     slices.Sorted(maps.Keys(section)),
		types:     make(map[string]string, len(section)),
		intervals: make(map[string]time.Duration, len(section)),
	}
	if resolution > 0 {
		catalog.cycles = make(map[string]int)
	}
	for _, name := range catalog.names {
		catalog.intervals[name] = resolution
		entry, ok := section[name].(map[string]any)
		if !ok {
			catalog.types[name] = ""
			continue
		}
		catalog.types[name], _ = entry[checks.CheckKeyType].(string)
		interval := cfgval.Duration(entry[config.EntryKeyInterval])
		if resolution <= 0 {
			catalog.intervals[name] = interval
			continue
		}
		if interval <= 0 {
			continue
		}
		cycles, warning := checks.ResolveInterval(interval, resolution)
		catalog.cycles[name] = cycles
		catalog.intervals[name] = time.Duration(cycles) * resolution
		if warning != "" {
			catalog.warnings = append(catalog.warnings, fmt.Sprintf("check %q %s", name, warning))
		}
	}
	return catalog
}
