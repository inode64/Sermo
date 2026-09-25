package servicemgr

import (
	"context"
	"fmt"
	"strings"
	"time"

	"sermo/internal/execx"
)

// UnitResolver resolves a service to the concrete unit name the active backend
// knows, trying the per-backend candidate names in order.
type UnitResolver struct {
	Runner  execx.Runner
	Probe   Probe
	Manager Manager
	Timeout time.Duration
}

// NewUnitResolver returns a resolver backed by the real host.
func NewUnitResolver() UnitResolver {
	return UnitResolver{Runner: execx.CommandRunner{}, Probe: OSProbe{}, Timeout: defaultDetectTimeout}
}

// Resolve picks the first candidate the backend actually knows, normalizing
// systemd unit names. With trust=true (a scalar/shorthand service)
// the first candidate is returned as-is when none can be probed, so units the
// probe cannot surface (e.g. sysv-generated) are not wrongly rejected. With
// trust=false (an explicit per-init list) it requires a match and otherwise
// fails; an empty candidate list means the service is not available on backend.
func (r UnitResolver) Resolve(ctx context.Context, backend Backend, candidates []string, trust bool) (string, error) {
	runner := execx.RunnerOrDefault(r.Runner)
	probe := r.Probe
	if probe == nil {
		probe = OSProbe{}
	}

	var tried []string
	var known []string
	seenUnits := map[string]struct{}{}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		var path string
		if backend == BackendOpenRC {
			var ok bool
			if path, ok = openRCUnitPath(openRCInitDir, candidate); !ok {
				return "", fmt.Errorf("invalid OpenRC unit %q", candidate)
			}
		}
		unit := NormalizeUnit(backend, candidate)
		if _, ok := seenUnits[unit]; ok {
			continue
		}
		seenUnits[unit] = struct{}{}
		tried = append(tried, unit)
		if r.knows(ctx, backend, unit, path, runner, probe) {
			known = append(known, unit)
		}
	}
	if active := r.activeKnownUnit(ctx, known); active != "" {
		return active, nil
	}
	if len(known) > 0 {
		return known[0], nil
	}

	if trust && len(tried) > 0 {
		return tried[0], nil
	}
	if len(tried) == 0 {
		return "", fmt.Errorf("service is not available on %s", backend)
	}
	return "", fmt.Errorf("no unit resolved on %s; tried: %s", backend, strings.Join(tried, ", "))
}

// knows reports whether the backend recognizes a candidate: systemd via
// `systemctl cat`, OpenRC via the presence of the init script.
func (r UnitResolver) knows(ctx context.Context, backend Backend, unit, path string, runner execx.Runner, probe Probe) bool {
	switch backend {
	case BackendSystemd:
		res, err := execx.Run(ctx, runner, r.timeout(), cmdSystemctl, systemctlCmdCat, commandArgTerminator, unit)
		return err == nil && res.ExitCode == execx.ExitCodeSuccess
	case BackendOpenRC:
		return probe.PathExists(path)
	default:
		return false
	}
}

func (r UnitResolver) activeKnownUnit(ctx context.Context, units []string) string {
	if r.Manager == nil {
		return ""
	}
	for _, unit := range units {
		status, err := r.Manager.Status(ctx, unit)
		if err == nil && status.Status == StatusActive {
			return unit
		}
	}
	return ""
}

func (r UnitResolver) timeout() time.Duration {
	if r.Timeout <= 0 {
		return defaultDetectTimeout
	}
	return r.Timeout
}
