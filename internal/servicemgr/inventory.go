package servicemgr

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"sermo/internal/execx"
	"sermo/internal/hostfs"
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
	openRCStateInactive         = "inactive"
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

// ActiveUnitSnapshot answers whether a unit is active from one listing of the
// backend's active units, taken at most once per TTL and shared by every
// caller. A host with many services then pays one init query per refresh
// instead of one per service and cycle; on OpenRC each `rc-service status`
// spawns the init script and its helpers, and some scripts run a costly
// status() of their own. A unit absent from the listing — stopped, crashed,
// transitional, an alias, or a failed listing — is not answered: the caller
// queries it live, so a stale listing can delay a failure verdict by at most
// one TTL but never invent one.
type ActiveUnitSnapshot struct {
	backend Backend
	runner  execx.Runner
	ttl     time.Duration
	timeout time.Duration
	now     func() time.Time

	mu    sync.Mutex
	at    time.Time
	units map[string]struct{}
}

// NewActiveUnitSnapshot returns a snapshot of backend's active units refreshed
// at most once per ttl, each listing bounded by timeout.
func NewActiveUnitSnapshot(backend Backend, runner execx.Runner, ttl, timeout time.Duration) *ActiveUnitSnapshot {
	return &ActiveUnitSnapshot{backend: backend, runner: runner, ttl: ttl, timeout: timeout, now: time.Now}
}

// Active reports whether the current listing names unit as active. Concurrent
// callers wait for a single refresh rather than each listing the units.
func (s *ActiveUnitSnapshot) Active(ctx context.Context, unit string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if now := s.now(); s.at.IsZero() || now.Sub(s.at) >= s.ttl {
		units, err := ListActiveUnits(ctx, s.backend, s.runner, s.timeout)
		s.at = now
		s.units = make(map[string]struct{}, len(units))
		if err == nil {
			for _, name := range units {
				s.units[name] = struct{}{}
			}
		}
	}
	_, ok := s.units[NormalizeUnit(s.backend, unit)]
	return ok
}

// ListStartedUnits returns the service units the init backend holds started,
// for discovery that needs unit names rather than health. On OpenRC it reads
// the started state directory instead of `rc-status --all`, whose daemon check
// for every service scans /proc and grows with the host's services; a started
// service whose daemon died is therefore listed too. systemd lists its active
// service units.
func ListStartedUnits(ctx context.Context, backend Backend, runner execx.Runner, timeout time.Duration) ([]string, error) {
	if backend != BackendOpenRC {
		return ListActiveUnits(ctx, backend, runner, timeout)
	}
	return listOpenRCStartedUnits(openRCStartedDir)
}

func listOpenRCStartedUnits(dir string) ([]string, error) {
	entries, err := hostfs.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("list started openrc units: %w", err)
	}
	units := make([]string, 0, len(entries))
	for _, entry := range entries {
		units = append(units, entry.Name())
	}
	return units, nil
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
	return strutil.Unique(out)
}

// ParseOpenRCActiveUnits extracts started services from rc-status output.
func ParseOpenRCActiveUnits(stdout string) []string {
	return parseOpenRCUnits(stdout, StatusActive)
}

// ParseOpenRCFailedUnits extracts crashed services from rc-status output.
// `crashed` is OpenRC's failure state: the service was started and its process
// is gone.
func ParseOpenRCFailedUnits(stdout string) []string {
	return parseOpenRCUnits(stdout, StatusFailed)
}

func parseOpenRCUnits(stdout string, wanted Status) []string {
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
		if name := openRCServiceWithStatus(line, wanted); name != "" {
			out = append(out, name)
		}
	}
	// A service can appear in more than one matched runlevel, and duplicates are
	// not guaranteed to be adjacent.
	return strutil.Unique(out)
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

func openRCServiceWithStatus(line string, wanted Status) string {
	status, ok := openRCLineStatus(line)
	if !ok || status != wanted {
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
