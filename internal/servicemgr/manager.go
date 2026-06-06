package servicemgr

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"sermo/internal/execx"
)

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
		return openrcManager{runner: runner}, nil
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
	ctx, cancel := context.WithTimeout(context.Background(), defaultDetectTimeout)
	defer cancel()

	res, err := runner.Run(ctx, "systemctl", "show", "-p", "MainPID", "--value", "--", unit)
	if err != nil {
		return 0, false
	}
	pid, perr := strconv.Atoi(strings.TrimSpace(res.Stdout))
	if perr != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// MainPIDFunc returns a process.Discoverer.MainPIDs closure for a unit, backed by
// the real host commands.
func MainPIDFunc(backend Backend, unit string) func() []int {
	return func() []int {
		if pid, ok := MainPID(execx.CommandRunner{}, backend, unit); ok {
			return []int{pid}
		}
		return nil
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
	runner execx.Runner
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
	case strings.Contains(out, "started"):
		return StatusActive
	case strings.Contains(out, "crashed"):
		return StatusFailed
	case strings.Contains(out, "stopped"):
		return StatusInactive
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
