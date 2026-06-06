package servicemgr

import (
	"context"
	"fmt"
	"strings"
	"time"

	"sermo/internal/execx"
)

// UnitResolver resolves a service to the concrete unit name the active backend
// knows, trying service.name then the per-backend aliases in order (section 11).
type UnitResolver struct {
	Runner  execx.Runner
	Probe   Probe
	Timeout time.Duration
}

// NewUnitResolver returns a resolver backed by the real host.
func NewUnitResolver() UnitResolver {
	return UnitResolver{Runner: execx.CommandRunner{}, Probe: OSProbe{}, Timeout: defaultDetectTimeout}
}

// Resolve picks the first candidate (service.name, then aliases) the backend
// actually knows, normalizing systemd unit names (section 11). When aliases are
// given but none resolve, it fails listing the candidates tried. When no aliases
// are given, it trusts service.name as-is, so units the probe cannot surface
// (e.g. sysv-generated) are not wrongly rejected.
func (r UnitResolver) Resolve(ctx context.Context, backend Backend, name string, aliases []string) (string, error) {
	runner := r.Runner
	if runner == nil {
		runner = execx.CommandRunner{}
	}
	probe := r.Probe
	if probe == nil {
		probe = OSProbe{}
	}

	var tried []string
	for _, candidate := range dedupeCandidates(name, aliases) {
		unit := candidate
		if backend == BackendSystemd {
			unit = systemdUnit(candidate)
		}
		tried = append(tried, unit)
		if r.knows(ctx, backend, unit, candidate, runner, probe) {
			return unit, nil
		}
	}

	if len(aliases) == 0 {
		if backend == BackendSystemd {
			return systemdUnit(name), nil
		}
		return name, nil
	}
	return "", fmt.Errorf("no unit resolved for %q on %s; tried: %s", name, backend, strings.Join(tried, ", "))
}

// knows reports whether the backend recognizes a candidate: systemd via
// `systemctl cat`, OpenRC via the presence of the init script.
func (r UnitResolver) knows(ctx context.Context, backend Backend, unit, candidate string, runner execx.Runner, probe Probe) bool {
	switch backend {
	case BackendSystemd:
		commandCtx, cancel := context.WithTimeout(ctx, r.timeout())
		defer cancel()
		res, err := runner.Run(commandCtx, "systemctl", "cat", "--", unit)
		return err == nil && res.ExitCode == 0
	case BackendOpenRC:
		return probe.PathExists("/etc/init.d/" + candidate)
	default:
		return false
	}
}

func (r UnitResolver) timeout() time.Duration {
	if r.Timeout <= 0 {
		return defaultDetectTimeout
	}
	return r.Timeout
}

func dedupeCandidates(name string, aliases []string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(c string) {
		if c == "" {
			return
		}
		if _, ok := seen[c]; ok {
			return
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	add(name)
	for _, a := range aliases {
		add(a)
	}
	return out
}
