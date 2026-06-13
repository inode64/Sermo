package servicemgr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"sermo/internal/execx"
)

// cgroupRoot is the unified cgroup v2 mount point.
const cgroupRoot = "/sys/fs/cgroup"

// ServiceStatus is the resolved status of a single service on a backend.
type ServiceStatus struct {
	Service string
	Backend Backend
	Unit    string
	Status  Status
}

// Manager queries and controls services on a specific backend.
//
// Start, Stop and Restart are raw backend actions: they invoke the underlying
// service manager and report whether it succeeded. They do NOT implement the
// safe operation engine (locks, guards, preflight, residual-process handling);
// that wraps these primitives separately.
type Manager interface {
	Status(ctx context.Context, service string) (ServiceStatus, error)
	Start(ctx context.Context, service string) error
	Stop(ctx context.Context, service string) error
	Restart(ctx context.Context, service string) error
	// Reload asks the init system to reload the service's configuration without a
	// full restart (systemd `reload` runs the unit's ExecReload, e.g. `udevadm
	// control --reload` or `nginx -s reload`; OpenRC runs the init script's
	// `reload`). A unit/script with no reload support surfaces as an action error.
	Reload(ctx context.Context, service string) error
	// SupportsReload reports whether the init backend can reload the unit in place:
	// systemd's `CanReload` (the unit defines `ExecReload`), or an OpenRC init
	// script that defines a `reload` command. It lets the operation engine decide
	// whether to use the backend reload or fall back to a native signal/command
	// (a `reload:` block with `when: auto`). Best-effort: a query error reports
	// false so the caller falls back to the native reload it was given.
	SupportsReload(ctx context.Context, service string) (bool, error)
	// ResetState reconciles the init system's recorded state with reality,
	// clearing a lingering failed/stuck marker so it no longer disagrees with the
	// actual processes (systemd `reset-failed`, OpenRC `zap`). It is idempotent
	// and a no-op when there is nothing to clear.
	ResetState(ctx context.Context, service string) error
}

// NewManager returns a Manager for backend using the real host commands.
func NewManager(backend Backend) (Manager, error) {
	return newManager(backend, execx.CommandRunner{})
}

func newManager(backend Backend, runner execx.Runner) (Manager, error) {
	switch backend {
	case BackendSystemd:
		return systemdManager{runner: runner}, nil
	case BackendOpenRC:
		return openrcManager{runner: runner, readFile: os.ReadFile}, nil
	default:
		return nil, fmt.Errorf("no service manager for backend %q", backend)
	}
}

// MainPID returns the backend's main process ID for a unit (section 21, step 1).
// systemd exposes it via `systemctl show -p MainPID`; OpenRC has no uniform
// equivalent, so it returns false there (pidfile selectors cover OpenRC).
func MainPID(runner execx.Runner, backend Backend, unit string) (int, bool) {
	if backend != BackendSystemd {
		return 0, false
	}
	if runner == nil {
		runner = execx.CommandRunner{}
	}
	res, err := execx.Run(context.Background(), runner, defaultDetectTimeout, "systemctl", "show", "-p", "MainPID", "--value", "--", unit)
	if err != nil {
		return 0, false
	}
	pid, perr := strconv.Atoi(strings.TrimSpace(res.Stdout))
	if perr != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// CgroupPIDs returns every PID in a unit's control group (section 21, step 1).
// systemd exposes the cgroup path via `systemctl show -p ControlGroup`, and all
// processes in it belong to the service — more complete than MainPID alone.
// readFile defaults to os.ReadFile.
func CgroupPIDs(runner execx.Runner, readFile func(string) ([]byte, error), backend Backend, unit string) ([]int, bool) {
	if backend != BackendSystemd {
		return nil, false
	}
	if runner == nil {
		runner = execx.CommandRunner{}
	}
	if readFile == nil {
		readFile = os.ReadFile
	}
	res, err := execx.Run(context.Background(), runner, defaultDetectTimeout, "systemctl", "show", "-p", "ControlGroup", "--value", "--", unit)
	if err != nil {
		return nil, false
	}
	cgroup := strings.TrimSpace(res.Stdout)
	if cgroup == "" || cgroup == "/" {
		return nil, false
	}

	data, err := readFile(filepath.Join(cgroupRoot, cgroup, "cgroup.procs"))
	if err != nil {
		return nil, false
	}
	var pids []int
	for _, line := range strings.Split(string(data), "\n") {
		if pid, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids, len(pids) > 0
}

// BackendPIDsFunc returns a process.Discoverer.BackendPIDs closure for a unit: it
// reports the cgroup process set (preferred) plus the MainPID, deduplicated,
// backed by the real host (section 21, step 1).
func BackendPIDsFunc(backend Backend, unit string) func() []int {
	return func() []int {
		seen := map[int]bool{}
		var pids []int
		add := func(pid int) {
			if pid > 0 && !seen[pid] {
				seen[pid] = true
				pids = append(pids, pid)
			}
		}
		if cg, ok := CgroupPIDs(execx.CommandRunner{}, os.ReadFile, backend, unit); ok {
			for _, pid := range cg {
				add(pid)
			}
		}
		if pid, ok := MainPID(execx.CommandRunner{}, backend, unit); ok {
			add(pid)
		}
		return pids
	}
}

// systemdManager queries services through systemctl.
type systemdManager struct {
	runner execx.Runner
}

func (m systemdManager) Status(ctx context.Context, service string) (ServiceStatus, error) {
	unit := systemdUnit(service)
	// `systemctl is-active` exits non-zero when the unit is not active but still
	// prints the state, so a non-zero exit is not a failure to query.
	result, err := m.runner.Run(ctx, "systemctl", "is-active", unit)
	state := strings.TrimSpace(result.Stdout)
	if state == "" && result.ExitCode < 0 {
		return ServiceStatus{}, fmt.Errorf("query systemd status for %s: %w", unit, err)
	}
	return ServiceStatus{
		Service: service,
		Backend: BackendSystemd,
		Unit:    unit,
		Status:  systemdStatus(state),
	}, nil
}

func (m systemdManager) Start(ctx context.Context, service string) error {
	return m.action(ctx, "start", service)
}

func (m systemdManager) Stop(ctx context.Context, service string) error {
	return m.action(ctx, "stop", service)
}

func (m systemdManager) Restart(ctx context.Context, service string) error {
	return m.action(ctx, "restart", service)
}

func (m systemdManager) Reload(ctx context.Context, service string) error {
	return m.action(ctx, "reload", service)
}

func (m systemdManager) ResetState(ctx context.Context, service string) error {
	return m.action(ctx, "reset-failed", service)
}

// SupportsReload queries systemd's CanReload property, which is true exactly when
// the unit defines an ExecReload (so `systemctl reload` is applicable).
func (m systemdManager) SupportsReload(ctx context.Context, service string) (bool, error) {
	unit := systemdUnit(service)
	result, err := m.runner.Run(ctx, "systemctl", "show", "-p", "CanReload", "--value", "--", unit)
	if err != nil && strings.TrimSpace(result.Stdout) == "" {
		return false, fmt.Errorf("query CanReload for %s: %w", unit, err)
	}
	return strings.EqualFold(strings.TrimSpace(result.Stdout), "yes"), nil
}

func (m systemdManager) action(ctx context.Context, verb, service string) error {
	unit := systemdUnit(service)
	result, err := m.runner.Run(ctx, "systemctl", verb, unit)
	if err != nil {
		return actionError(fmt.Sprintf("systemctl %s %s", verb, unit), result, err)
	}
	return nil
}

// openrcManager queries services through rc-service.
type openrcManager struct {
	runner   execx.Runner
	readFile func(string) ([]byte, error)
}

func (m openrcManager) Status(ctx context.Context, service string) (ServiceStatus, error) {
	// `rc-service SERVICE status` exits non-zero when stopped/crashed but reports
	// the state on stdout, so a non-zero exit is not a failure to query.
	result, err := m.runner.Run(ctx, "rc-service", service, "status")
	if result.ExitCode < 0 && strings.TrimSpace(result.Stdout) == "" {
		return ServiceStatus{}, fmt.Errorf("query openrc status for %s: %w", service, err)
	}
	return ServiceStatus{
		Service: service,
		Backend: BackendOpenRC,
		Unit:    service,
		Status:  openrcStatus(result),
	}, nil
}

func (m openrcManager) Start(ctx context.Context, service string) error {
	return m.action(ctx, "start", service)
}

func (m openrcManager) Stop(ctx context.Context, service string) error {
	return m.action(ctx, "stop", service)
}

func (m openrcManager) Restart(ctx context.Context, service string) error {
	return m.action(ctx, "restart", service)
}

func (m openrcManager) Reload(ctx context.Context, service string) error {
	return m.action(ctx, "reload", service)
}

func (m openrcManager) ResetState(ctx context.Context, service string) error {
	return m.action(ctx, "zap", service)
}

// openrcReloadDef matches an OpenRC init script that defines a reload command:
// a `reload()` function, `reload` listed in extra_(started_)commands, or a
// `description_reload=` line (the documented ways an init script exposes reload).
// Every alternative is anchored at the start of a line (after leading blanks) so
// it cannot match a comment (`# ...`), and the `reload` token in the command list
// is bounded by a quote/space so `forcereload` is not a false positive — a false
// positive is worse than a false negative here, because the `when: auto` path
// then runs the init reload (which fails) instead of the native one.
var openrcReloadDef = regexp.MustCompile(`(?m)` +
	`^[[:space:]]*reload[[:space:]]*\(\)` + // reload() / reload ()
	`|^[[:space:]]*extra_(started_)?commands=.*["[:space:]]reload(["[:space:]]|$)` + // reload as a listed command
	`|^[[:space:]]*description_reload=`) // documented reload description

// SupportsReload reports whether the OpenRC init script for the service defines a
// reload command. The script lives at /etc/init.d/<service>; an unreadable script
// reports false (best-effort) so the caller falls back to its native reload.
func (m openrcManager) SupportsReload(_ context.Context, service string) (bool, error) {
	read := m.readFile
	if read == nil {
		read = os.ReadFile
	}
	data, err := read(filepath.Join("/etc/init.d", service))
	if err != nil {
		return false, nil //nolint:nilerr // unreadable scripts mean reload support is unknown; callers fall back safely
	}
	return openrcReloadDef.Match(data), nil
}

func (m openrcManager) action(ctx context.Context, verb, service string) error {
	result, err := m.runner.Run(ctx, "rc-service", service, verb)
	if err != nil {
		return actionError(fmt.Sprintf("rc-service %s %s", service, verb), result, err)
	}
	return nil
}

// actionError builds an error for a failed backend action, preferring the
// command's stderr/stdout for a useful message and falling back to the raw
// runner error (which carries the exit code).
func actionError(command string, result execx.Result, err error) error {
	if msg := strings.TrimSpace(result.Stderr); msg != "" {
		return fmt.Errorf("%s: %s", command, msg)
	}
	if msg := strings.TrimSpace(result.Stdout); msg != "" {
		return fmt.Errorf("%s: %s", command, msg)
	}
	return fmt.Errorf("%s: %w", command, err)
}

// systemdUnitSuffixes are the unit types systemd recognizes; a service name that
// already carries one of these is used verbatim.
var systemdUnitSuffixes = []string{
	".service", ".socket", ".target", ".mount", ".automount",
	".swap", ".path", ".timer", ".slice", ".scope", ".device",
}

// systemdUnit normalizes a service name to a systemd unit, appending `.service`
// when the name has no unit suffix (nginx -> nginx.service).
func systemdUnit(service string) string {
	for _, suffix := range systemdUnitSuffixes {
		if strings.HasSuffix(service, suffix) {
			return service
		}
	}
	return service + ".service"
}

func systemdStatus(state string) Status {
	switch state {
	case "active":
		return StatusActive
	case "failed":
		return StatusFailed
	case "inactive", "deactivating":
		return StatusInactive
	default:
		// activating, reloading, unknown and empty states are not a clean active.
		return StatusUnknown
	}
}

func openrcStatus(result execx.Result) Status {
	out := strings.ToLower(result.Stdout)
	switch {
	case strings.Contains(out, "crashed"):
		return StatusFailed
	case strings.Contains(out, "stopped"), strings.Contains(out, "not started"):
		return StatusInactive
	case strings.Contains(out, "started"):
		return StatusActive
	}
	switch result.ExitCode {
	case 0:
		return StatusActive
	case 3:
		return StatusInactive
	default:
		return StatusUnknown
	}
}
