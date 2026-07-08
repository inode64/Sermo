package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"sermo/internal/app"
	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/locks"
	"sermo/internal/mountctl"
	"sermo/internal/state"
)

const mountCommandTargetArgCount = 2

func (a App) runMount(ctx context.Context, opts options) int {
	if len(opts.args) == 0 {
		return a.commandUsageError(commandMount, "mount requires a target, or subcommand status/list")
	}
	switch opts.args[0] {
	case commandMountList:
		if len(opts.args) > 1 {
			return a.commandUsageError(commandMount, "mount list takes no arguments")
		}
		return a.runMountList(opts)
	case commandStatus:
		if len(opts.args) != mountCommandTargetArgCount {
			return a.commandUsageError(commandMount, "mount status requires exactly one mount name or path")
		}
		return a.runMountStatus(opts, opts.args[1])
	default:
		if len(opts.args) > 1 {
			return a.commandUsageError(commandMount, "mount takes exactly one target")
		}
		return a.runMountAcquire(ctx, opts, opts.args[0])
	}
}

func (a App) runUmount(ctx context.Context, opts options) int {
	if len(opts.args) == 0 {
		return a.commandUsageError(commandUmount, "umount requires a mount name or path")
	}
	if len(opts.args) > 1 {
		return a.commandUsageError(commandUmount, "umount takes exactly one mount name or path")
	}
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	spec, code := a.mountSpec(opts, cfg, opts.args[0])
	if code != exitSuccess {
		return code
	}
	controller := a.mountController(cfg, opts)
	res, err := controller.Release(ctx, spec)
	a.syncStorageMountMonitoring(opts, cfg, spec.Name, mountctl.ActionUmount, err == nil && res.Status == mountctl.ResultOK)
	if err != nil {
		return a.printMountResult(opts, res, err)
	}
	return a.printMountResult(opts, res, nil)
}

func (a App) runMountAcquire(ctx context.Context, opts options, target string) int {
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	spec, code := a.mountSpec(opts, cfg, target)
	if code != exitSuccess {
		return code
	}
	controller := a.mountController(cfg, opts)
	res, err := controller.Acquire(ctx, spec)
	a.syncStorageMountMonitoring(opts, cfg, spec.Name, mountctl.ActionMount, err == nil && res.Status == mountctl.ResultOK)
	if err != nil {
		return a.printMountResult(opts, res, err)
	}
	return a.printMountResult(opts, res, nil)
}

func (a App) runMountStatus(opts options, target string) int {
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	spec, code := a.mountSpec(opts, cfg, target)
	if code != exitSuccess {
		return code
	}
	status, err := a.mountController(cfg, opts).ReadStatus(spec)
	if err != nil {
		return a.fail(opts, err.Error())
	}
	if opts.json {
		writeJSON(a.Stdout, status)
		return exitSuccess
	}
	fmt.Fprintf(a.Stdout, "name: %s\npath: %s\nmounted: %t\nrefcount: %d\nstate: %s\n",
		status.Name, status.Path, status.Mounted, status.Refcount, status.State)
	return exitSuccess
}

func (a App) runMountList(opts options) int {
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	controller := a.mountController(cfg, opts)
	names := cfg.StorageMountNames()
	sort.Strings(names)
	var statuses []mountctl.Status
	for _, name := range names {
		resolved, errs := cfg.ResolveStorage(name)
		if len(errs) > 0 {
			continue
		}
		status, err := controller.ReadStatus(mountctl.SpecFromStorageTree(name, resolved.Tree))
		if err == nil {
			statuses = append(statuses, status)
		}
	}
	if opts.json {
		writeJSON(a.Stdout, statuses)
		return exitSuccess
	}
	if len(statuses) == 0 {
		fmt.Fprintln(a.Stdout, "no configured mounts")
		return exitSuccess
	}
	for _, st := range statuses {
		fmt.Fprintf(a.Stdout, "%s path=%s mounted=%t refcount=%d state=%s\n", st.Name, st.Path, st.Mounted, st.Refcount, st.State)
	}
	return exitSuccess
}

func (a App) mountSpec(opts options, cfg *config.Config, target string) (mountctl.Spec, int) {
	if filepath.IsAbs(target) {
		if name := cfg.StorageNameByPath(target); name != "" {
			return a.configuredMountSpec(opts, cfg, name)
		}
		return mountctl.EphemeralSpec(target), exitSuccess
	}
	return a.configuredMountSpec(opts, cfg, target)
}

func (a App) configuredMountSpec(opts options, cfg *config.Config, name string) (mountctl.Spec, int) {
	resolved, errs := cfg.ResolveStorage(name)
	if len(errs) > 0 {
		a.printIssues(opts, scopedIssues("watch "+name, errs))
		return mountctl.Spec{}, exitConfigInvalid
	}
	if _, ok := resolved.Tree[config.StorageKeyMount].(map[string]any); !ok {
		a.reportError(opts, fmt.Sprintf("storage watch %q has no mount block", name))
		return mountctl.Spec{}, exitRuntimeError
	}
	return mountctl.SpecFromStorageTree(name, resolved.Tree), exitSuccess
}

func (a App) mountController(cfg *config.Config, opts options) mountctl.Controller {
	lookup := app.EngineUserLookup(cfg, a.Runner)
	if a.MountController != nil {
		c := a.MountController(cfg)
		if c.CommandTimeout <= 0 {
			c.CommandTimeout = opts.timeout
		}
		if c.ResolveUser == nil {
			c.ResolveUser = lookup.ResolveUser
		}
		if c.UserLookup == nil {
			c.UserLookup = lookup
		}
		return c
	}
	return mountctl.Controller{Runtime: cfg.Global.RuntimeDir(), Runner: a.Runner, ResolveUser: lookup.ResolveUser, UserLookup: lookup, CommandTimeout: opts.timeout}
}

func (a App) syncStorageMountMonitoring(opts options, cfg *config.Config, storage, action string, resultOK bool) {
	monitorMode, disabled, ok := storageMountWatchConfig(cfg, storage)
	if !ok {
		return
	}
	store, err := state.Open(filepath.Join(cfg.Global.StateDir(), state.Filename))
	if err != nil {
		msg := fmt.Sprintf("storage mount monitoring unavailable: %v", err)
		fmt.Fprintf(a.Stderr, "warning: %s\n", msg)
		a.recordAccess(cfg, action+"-monitor", storage, accessStatusError, msg)
		return
	}
	defer store.Close()
	change, err := app.SyncStorageMountMonitoring(store, storage, action, resultOK, monitorMode, disabled, state.SourceCLIMountUmount, state.SourceCLI)
	if err != nil {
		msg := err.Error()
		fmt.Fprintf(a.Stderr, "warning: %s\n", msg)
		a.recordAccess(cfg, action+"-monitor", storage, accessStatusError, msg)
		return
	}
	if change.Changed {
		a.recordAccess(cfg, change.Action, storage, accessStatusOK, change.Message)
	}
}

func storageMountWatchConfig(cfg *config.Config, storage string) (monitorMode string, disabled bool, ok bool) {
	if cfg == nil {
		return "", false, false
	}
	resolved, errs := cfg.ResolveStorage(storage)
	if len(errs) > 0 || resolved.Tree == nil {
		return "", false, false
	}
	watches, _ := cfg.ResolveWatches()
	entry, _ := watches[storage].(map[string]any)
	if entry == nil {
		return "", false, false
	}
	check, _ := entry[config.WatchKeyCheck].(map[string]any)
	if cfgval.AsString(check[checks.CheckKeyType]) != checks.CheckTypeStorage {
		return "", false, false
	}
	return config.MonitorMode(entry), cfgval.Disabled(entry), true
}

func (a App) printMountResult(opts options, res mountctl.Result, err error) int {
	if opts.json {
		if err != nil && res.Name == "" {
			writeJSON(a.Stdout, map[string]string{cliJSONKeyError: err.Error()})
		} else {
			writeJSON(a.Stdout, res)
		}
		return mountExitCode(err)
	}
	if err != nil && res.Name == "" {
		fmt.Fprintln(a.Stderr, err)
		return mountExitCode(err)
	}
	if res.Message != "" {
		fmt.Fprintf(a.Stdout, "%s: %s", res.Name, res.Message)
	} else {
		fmt.Fprintf(a.Stdout, "%s: %s %s", res.Name, res.Action, res.Status)
	}
	fmt.Fprintf(a.Stdout, ", refcount=%d", res.Refcount)
	if res.Lazy {
		fmt.Fprint(a.Stdout, ", lazy=true")
	}
	fmt.Fprintln(a.Stdout)
	for _, p := range res.Blockers {
		key, value := processDisplayField(p)
		fmt.Fprintf(a.Stdout, "  blocker pid=%d %s=%s\n", p.PID, key, value)
	}
	return mountExitCode(err)
}

func mountExitCode(err error) int {
	if err == nil {
		return exitSuccess
	}
	var held *locks.HeldError
	if errors.As(err, &held) {
		return exitBlocked
	}
	return exitRuntimeError
}
