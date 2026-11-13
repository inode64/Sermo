package checks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
	"sermo/internal/hostfs"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

// Explicit service targets share these fields across service and host checks.
const (
	CheckKeyService            = "service"
	CheckKeyExpectResult       = "expect_result"
	systemdUnitInterface       = "org.freedesktop.systemd1.Unit"
	systemdServiceInterface    = "org.freedesktop.systemd1.Service"
	systemdPropertyLoadState   = "LoadState"
	systemdPropertyActiveState = "ActiveState"
	systemdPropertyResult      = "Result"
	systemdLoadStateLoaded     = "loaded"
)

func unitServiceFields(entry map[string]any) error {
	units := cfgval.StringList(entry[CheckKeyService])
	backend := servicemgr.Backend(cfgval.AsString(entry[CheckKeyBackend]))
	if !cfgval.IsNonEmptyStringList(entry[CheckKeyService]) {
		return errors.New("service must name an init unit or a non-empty list of units")
	}
	for _, unit := range units {
		if !servicemgr.ValidOpenRCUnit(unit) {
			return errors.New("service must name valid init units")
		}
	}
	if backend != servicemgr.BackendOpenRC && backend != servicemgr.BackendSystemd {
		return errors.New("backend must be openrc or systemd for an explicit service")
	}
	return nil
}

func buildUnitServiceCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	units := cfgval.StringList(entry[CheckKeyService])
	backend := servicemgr.Backend(cfgval.AsString(entry[CheckKeyBackend]))
	expect := cfgval.AsString(entry[CheckKeyExpect])
	observe := deps.ProcessesAny
	if observe == nil {
		observe = (process.Discoverer{}).ObserveAnyState
	}
	read := func(ctx context.Context, unit string) (servicemgr.Status, error) {
		if backend == servicemgr.BackendSystemd {
			return systemdUnitState(ctx, unit, conn.SystemdUnitProperty)
		}
		return servicemgr.ObserveOpenRCStatus(ctx, unit, func(ctx context.Context, info servicemgr.ProcInfo) (bool, error) {
			return openRCDaemonRunning(ctx, info, observe, (process.OSReader{}).IdentityWithError)
		})
	}
	if result := cfgval.AsString(entry[CheckKeyExpectResult]); result != "" {
		expect = result
		read = func(ctx context.Context, unit string) (servicemgr.Status, error) {
			return systemdUnitResult(ctx, unit, conn.SystemdUnitProperty)
		}
	}
	status := func(ctx context.Context) (servicemgr.Status, error) {
		return anyUnitState(ctx, units, expect, read)
	}
	return serviceCheck{base: b, expect: expect, status: status}, ""
}

// A verified match proves the disjunction even if another unit is unavailable.
// Without a match, incomplete observations must remain unavailable for guards.
func anyUnitState(ctx context.Context, units []string, expect string, read func(context.Context, string) (servicemgr.Status, error)) (servicemgr.Status, error) {
	state := servicemgr.StatusUnknown
	var failures []error
	for _, unit := range units {
		if err := ctx.Err(); err != nil {
			return servicemgr.StatusUnknown, fmt.Errorf("observe units: %w", err)
		}
		observed, err := read(ctx, unit)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if string(observed) == expect {
			return observed, nil
		}
		state = observed
	}
	return state, errors.Join(failures...)
}

type systemdPropertyReader func(context.Context, string, string, string) (any, error)

func loadedSystemdUnit(ctx context.Context, unit string, read systemdPropertyReader) (string, error) {
	unit = servicemgr.NormalizeUnit(servicemgr.BackendSystemd, unit)
	loaded, err := read(ctx, unit, systemdUnitInterface, systemdPropertyLoadState)
	if err != nil {
		return "", fmt.Errorf("read unit load state: %w", err)
	}
	if loaded != systemdLoadStateLoaded {
		return "", fmt.Errorf("unit %s is not loaded: %v", unit, loaded)
	}
	return unit, nil
}

func systemdUnitState(ctx context.Context, unit string, read systemdPropertyReader) (servicemgr.Status, error) {
	unit, err := loadedSystemdUnit(ctx, unit, read)
	if err != nil {
		return servicemgr.StatusUnknown, err
	}
	value, err := read(ctx, unit, systemdUnitInterface, systemdPropertyActiveState)
	if err != nil {
		return servicemgr.StatusUnknown, fmt.Errorf("read unit active state: %w", err)
	}
	state, ok := value.(string)
	if !ok {
		return servicemgr.StatusUnknown, errors.New("unit active state is not a string")
	}
	switch state {
	case string(servicemgr.StatusActive), string(servicemgr.StatusInactive), string(servicemgr.StatusFailed):
		return servicemgr.Status(state), nil
	default:
		return servicemgr.StatusUnknown, fmt.Errorf("unit %s has transitional state %s", unit, state)
	}
}

func systemdUnitResult(ctx context.Context, unit string, read systemdPropertyReader) (servicemgr.Status, error) {
	unit, err := loadedSystemdUnit(ctx, unit, read)
	if err != nil {
		return servicemgr.StatusUnknown, err
	}
	value, err := read(ctx, unit, systemdServiceInterface, systemdPropertyResult)
	if err != nil {
		return servicemgr.StatusUnknown, fmt.Errorf("read unit result: %w", err)
	}
	result, ok := value.(string)
	if !ok || result == "" {
		return servicemgr.StatusUnknown, errors.New("unit result is not a non-empty string")
	}
	return servicemgr.Status(result), nil
}

func openRCDaemonRunning(ctx context.Context, info servicemgr.ProcInfo, observe func(context.Context, []string, string, *regexp.Regexp) string, identity func(int) (process.Identity, bool, error)) (bool, error) {
	if info.Pidfile != "" {
		pid, err := hostfs.ReadPidfile(info.Pidfile)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("read daemon pidfile: %w", err)
		}
		id, present, err := identity(pid)
		if err != nil {
			return false, fmt.Errorf("read daemon identity: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return false, fmt.Errorf("observe daemon PID: %w", err)
		}
		return present && id.State != process.ProcStateZombie, nil
	}
	pattern, err := regexp.Compile(info.Cmd)
	if err != nil {
		return false, fmt.Errorf("read daemon command: %w", err)
	}
	state := observe(ctx, nil, "", pattern)
	if state == process.StateUnknown {
		return false, errors.New("OpenRC daemon process observation is incomplete")
	}
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("observe daemon command: %w", err)
	}
	return state == process.StateRunning, nil
}
