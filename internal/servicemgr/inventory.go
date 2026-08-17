package servicemgr

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"time"

	"sermo/internal/execx"
	"sermo/internal/strutil"
)

const (
	openRCRunlevelPrefix        = "runlevel:"
	openRCDynamicRunlevelPrefix = "dynamic runlevel:"
	openRCRunlevelDefault       = "default"
	openRCRunlevelNeededWanted  = "needed/wanted"
	openRCRunlevelManual        = "manual"
	openRCRunlevelHotplugged    = "hotplugged"
	openRCStateStarted          = "started"
	openRCStateNotStarted       = "not started"
	openRCStateStopped          = "stopped"
	openRCStateCrashed          = "crashed"
	systemdListUnitNameIndex    = 0
	openRCServiceNameIndex      = 0
)

// unitListing is one unit listing per init backend: the argv that produces it
// and the parser for that backend's output. The two listings differ only in
// those, so they share one runner.
type unitListing struct {
	state        Status
	systemdArgs  []string
	openRCArgs   []string
	parseSystemd func(stdout string) []string
	parseOpenRC  func(stdout string) []string
}

var (
	activeUnitListing = unitListing{
		state:        StatusActive,
		systemdArgs:  []string{systemctlCmdListUnits, systemctlFlagTypeService, systemctlFlagStateActive, systemctlFlagNoLegend, systemctlFlagNoPager},
		openRCArgs:   []string{openRCFlagAll},
		parseSystemd: ParseSystemdActiveUnits,
		parseOpenRC:  ParseOpenRCActiveUnits,
	}
	// Not restricted to `.service`: a failed mount, timer or socket unit is a
	// fault too.
	failedUnitListing = unitListing{
		state:        StatusFailed,
		systemdArgs:  []string{systemctlCmdListUnits, systemctlFlagStateFailed, systemctlFlagNoLegend, systemctlFlagPlain, systemctlFlagNoPager},
		openRCArgs:   []string{openRCFlagAll},
		parseSystemd: ParseSystemdFailedUnits,
		parseOpenRC:  ParseOpenRCFailedUnits,
	}
)

// ListActiveUnits returns active service units for the selected init backend.
func ListActiveUnits(ctx context.Context, backend Backend, runner execx.Runner, timeout time.Duration) ([]string, error) {
	return listUnits(ctx, backend, runner, timeout, activeUnitListing)
}

// ListFailedUnits returns the units the selected init backend reports as failed.
//
// It is the only view Sermo has of a unit with no catalog profile — a site-local
// backup job, for instance — which is otherwise invisible to monitoring.
func ListFailedUnits(ctx context.Context, backend Backend, runner execx.Runner, timeout time.Duration) ([]string, error) {
	return listUnits(ctx, backend, runner, timeout, failedUnitListing)
}

func listUnits(ctx context.Context, backend Backend, runner execx.Runner, timeout time.Duration, listing unitListing) ([]string, error) {
	runner = execx.RunnerOrDefault(runner)
	var command string
	var args []string
	var parse func(string) []string
	switch backend {
	case BackendSystemd:
		command, args, parse = cmdSystemctl, listing.systemdArgs, listing.parseSystemd
	case BackendOpenRC:
		command, args, parse = cmdRcStatus, listing.openRCArgs, listing.parseOpenRC
	default:
		return nil, fmt.Errorf("no %s-unit listing for backend %q", listing.state, backend)
	}
	res, err := execx.Run(ctx, runner, timeout, command, args...)
	if err != nil && strings.TrimSpace(res.Stdout) == "" {
		return nil, fmt.Errorf("list %s %s units: %w", listing.state, backend, err)
	}
	return parse(res.Stdout), nil
}

// ParseSystemdActiveUnits extracts active .service units from systemctl output.
func ParseSystemdActiveUnits(stdout string) []string {
	return parseSystemdUnitNames(stdout, func(name string) bool {
		return strings.HasSuffix(name, systemdServiceSuffix)
	})
}

// ParseSystemdFailedUnits extracts every unit name from a `--state=failed`
// listing, whatever its unit type.
func ParseSystemdFailedUnits(stdout string) []string {
	return parseSystemdUnitNames(stdout, func(string) bool { return true })
}

func parseSystemdUnitNames(stdout string, keep func(name string) bool) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(stdout))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) <= systemdListUnitNameIndex || fields[systemdListUnitNameIndex] == systemdUnitHeader {
			continue
		}
		name := fields[systemdListUnitNameIndex]
		// Older systemd prints the status bullet even with --plain, and it is a
		// field of its own; the unit name is then the next one.
		if name == systemdUnitStatusBullet {
			if len(fields) <= systemdListUnitNameIndex+1 {
				continue
			}
			name = fields[systemdListUnitNameIndex+1]
		}
		if keep(name) {
			out = append(out, name)
		}
	}
	return strutil.MergeUnique(nil, out...)
}

// ParseOpenRCActiveUnits extracts started services from rc-status output.
func ParseOpenRCActiveUnits(stdout string) []string {
	return parseOpenRCUnits(stdout, openRCStartedService)
}

// ParseOpenRCFailedUnits extracts crashed services from rc-status output.
// `crashed` is OpenRC's failure state: the service was started and its process
// is gone.
func ParseOpenRCFailedUnits(stdout string) []string {
	return parseOpenRCUnits(stdout, openRCCrashedService)
}

func parseOpenRCUnits(stdout string, service func(line string) string) []string {
	var out []string
	inServiceRunlevel := false
	sc := bufio.NewScanner(strings.NewReader(stdout))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if name, ok := openRCRunlevel(line); ok {
			inServiceRunlevel = openRCServiceRunlevel(name)
			continue
		}
		if !inServiceRunlevel {
			continue
		}
		if name := service(line); name != "" {
			out = append(out, name)
		}
	}
	// A service can appear in more than one matched runlevel, and duplicates are
	// not guaranteed to be adjacent.
	return strutil.MergeUnique(nil, out...)
}

func openRCRunlevel(line string) (string, bool) {
	lower := strings.ToLower(line)
	if name, ok := strings.CutPrefix(lower, openRCRunlevelPrefix); ok {
		return strings.TrimSpace(name), true
	}
	if name, ok := strings.CutPrefix(lower, openRCDynamicRunlevelPrefix); ok {
		return strings.TrimSpace(name), true
	}
	return "", false
}

func openRCServiceRunlevel(name string) bool {
	switch name {
	case openRCRunlevelDefault, openRCRunlevelNeededWanted, openRCRunlevelManual, openRCRunlevelHotplugged:
		return true
	default:
		return false
	}
}

func openRCStartedService(line string) string {
	lower := strings.ToLower(line)
	if !strings.Contains(lower, openRCStateStarted) {
		return ""
	}
	if strings.Contains(lower, openRCStateNotStarted) ||
		strings.Contains(lower, openRCStateStopped) ||
		strings.Contains(lower, openRCStateCrashed) {
		return ""
	}
	return openRCServiceName(line)
}

func openRCCrashedService(line string) string {
	if !strings.Contains(strings.ToLower(line), openRCStateCrashed) {
		return ""
	}
	return openRCServiceName(line)
}

// openRCServiceName reads the service name from an rc-status line, which is the
// first field before the bracketed state.
func openRCServiceName(line string) string {
	beforeState, _, _ := strings.Cut(line, "[")
	fields := strings.Fields(beforeState)
	if len(fields) <= openRCServiceNameIndex {
		return ""
	}
	return fields[openRCServiceNameIndex]
}
