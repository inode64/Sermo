package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"sermo/internal/config"
	"sermo/internal/locks"
	"sermo/internal/mountctl"
)

func (a App) runMount(ctx context.Context, opts options) int {
	if len(opts.args) == 0 {
		return a.usageError("mount requires a target, or subcommand status/list")
	}
	switch opts.args[0] {
	case "list":
		return a.runMountList(opts)
	case "status":
		if len(opts.args) < 2 {
			return a.usageError("mount status requires a mount name or path")
		}
		return a.runMountStatus(opts, opts.args[1])
	default:
		return a.runMountAcquire(ctx, opts, opts.args[0])
	}
}

func (a App) runUmount(ctx context.Context, opts options) int {
	if len(opts.args) == 0 {
		return a.usageError("umount requires a mount name or path")
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
	fmt.Fprintf(a.Stdout, "name: %s\npath: %s\nmounted: %t\nrefcount: %d\nsource: %s\nstate: %s\n",
		status.Name, status.Path, status.Mounted, status.Refcount, status.Source, status.State)
	return exitSuccess
}

func (a App) runMountList(opts options) int {
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	controller := a.mountController(cfg, opts)
	names := append([]string(nil), cfg.MountNames...)
	sort.Strings(names)
	var statuses []mountctl.Status
	for _, name := range names {
		resolved, errs := cfg.ResolveMount(name)
		if len(errs) > 0 {
			continue
		}
		status, err := controller.ReadStatus(mountctl.SpecFromTree(name, resolved.Tree))
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
		if name := cfg.MountNameByPath(target); name != "" {
			return a.configuredMountSpec(opts, cfg, name)
		}
		return mountctl.EphemeralSpec(target), exitSuccess
	}
	return a.configuredMountSpec(opts, cfg, target)
}

func (a App) configuredMountSpec(opts options, cfg *config.Config, name string) (mountctl.Spec, int) {
	if _, ok := cfg.Mounts[name]; !ok {
		a.reportError(opts, fmt.Sprintf("unknown mount %q", name))
		return mountctl.Spec{}, exitRuntimeError
	}
	resolved, errs := cfg.ResolveMount(name)
	if len(errs) > 0 {
		a.printIssues(opts, scopedIssues("mount "+name, errs))
		return mountctl.Spec{}, exitConfigInvalid
	}
	return mountctl.SpecFromTree(name, resolved.Tree), exitSuccess
}

func (a App) mountController(cfg *config.Config, opts options) mountctl.Controller {
	if a.MountController != nil {
		c := a.MountController(cfg)
		if c.CommandTimeout <= 0 {
			c.CommandTimeout = opts.timeout
		}
		return c
	}
	return mountctl.Controller{Runtime: cfg.Global.RuntimeDir(), Runner: a.Runner, CommandTimeout: opts.timeout}
}

func (a App) printMountResult(opts options, res mountctl.Result, err error) int {
	if opts.json {
		if err != nil && res.Name == "" {
			writeJSON(a.Stdout, map[string]string{"error": err.Error()})
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
