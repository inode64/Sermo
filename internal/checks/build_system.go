package checks

import (
	"sermo/internal/cfgval"
	"sermo/internal/execx"
)

// buildLevelCheck runs the shared prologue of every check configured only by
// level predicates over fields, handing the parsed predicates to build. It is
// the whole body of the kernel-resource counter builders (users, fds, memory,
// pids, conntrack), which differ only in their field list and the check they
// construct.
func buildLevelCheck(entry map[string]any, fields []string, label string, build func([]levelPred) Check) (Check, string) {
	preds, errs := requireLevelPreds(entry, fields, label)
	if errs != "" {
		return nil, errs
	}
	return build(preds), ""
}

// buildSingleLevelCheck is buildLevelCheck for the checks whose field list holds
// exactly one predicate (entropy, zombies).
func buildSingleLevelCheck(entry map[string]any, fields []string, label string, build func(levelPred) Check) (Check, string) {
	pred, errs := requireSingleLevelPred(entry, fields, label)
	if errs != "" {
		return nil, errs
	}
	return build(pred), ""
}

// buildLoadCheck builds a system load-average check.
func buildLoadCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	preds, errs := requireLevelPreds(entry, LoadPredFields, "load check")
	if errs != "" {
		return nil, errs
	}
	return loadCheck{base: b, preds: preds, perCPU: cfgval.Bool(entry[CheckKeyPerCPU]), sampler: deps.LoadSampler}, ""
}

// buildUsersCheck builds a logged-in-user count check.
func buildUsersCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	return buildLevelCheck(entry, UsersPredFields, "users check", func(preds []levelPred) Check {
		return usersCheck{base: b, preds: preds, sampler: deps.UsersSampler}
	})
}

// buildSSHIdleCheck builds an interactive-SSH terminal idle check.
func buildSSHIdleCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	idleFor := cfgval.Duration(entry[CheckKeyIdleFor])
	if idleFor <= 0 {
		return nil, "ssh_idle check requires a positive idle_for duration"
	}
	sshdExes := cfgval.StringList(entry[CheckKeySSHDExe])
	if len(sshdExes) == 0 {
		return nil, "ssh_idle check requires sshd_exe"
	}
	preds, err := requireLevelPreds(entry, SSHIdlePredFields, "ssh_idle check")
	if err != "" {
		return nil, err
	}
	protected, parseErr := parseProtectedProcesses(entry[CheckKeyProtectedProcesses])
	if parseErr != nil {
		return nil, "ssh_idle protected_processes: " + parseErr.Error()
	}
	return sshIdleCheck{
		base:  b,
		preds: preds,
		config: SSHIdleConfig{
			IdleFor:            idleFor,
			SSHDExes:           sshdExes,
			ProtectedProcesses: protected,
		},
		sampler: deps.SSHIdleSampler,
	}, ""
}

// buildProcessCountCheck builds a check on the number of processes matching an
// optional user/exe/exe_dir filter.
func buildProcessCountCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	preds, errs := requireLevelPreds(entry, ProcessCountPredFields, "process_count check")
	if errs != "" {
		return nil, errs
	}
	return processCountCheck{
		base:   b,
		preds:  preds,
		user:   cfgval.AsString(entry[CheckKeyUser]),
		exe:    cfgval.AsString(entry[CheckKeyExe]),
		exeDir: cfgval.AsString(entry[CheckKeyExeDir]),
		count:  deps.ProcessCount,
	}, ""
}

// buildHdparmCheck builds a disk-throughput check (hdparm -t/-T).
func buildHdparmCheck(b base, entry map[string]any, runner execx.Runner) (Check, string) {
	device := cfgval.AsString(entry[CheckKeyDevice])
	if device == "" {
		return nil, "hdparm check requires a device"
	}
	preds, errs := requireLevelPreds(entry, HdparmPredFields, "hdparm check")
	if errs != "" {
		return nil, errs
	}
	return hdparmCheck{base: b, runner: runner, device: device, preds: preds}, ""
}

// buildSensorsCheck builds a hardware-sensor check (hwmon temp/fan/voltage).
func buildSensorsCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	preds, errs := requireLevelPreds(entry, SensorPredFields, "sensors check")
	if errs != "" {
		return nil, errs
	}
	return sensorsCheck{base: b, chip: cfgval.AsString(entry[CheckKeyChip]), label: cfgval.AsString(entry[CheckKeyLabel]), preds: preds, sampler: deps.SensorSampler}, ""
}

// buildSmartCheck builds a drive SMART-health check (smartctl).
func buildSmartCheck(b base, entry map[string]any, runner execx.Runner) (Check, string) {
	device := cfgval.AsString(entry[CheckKeyDevice])
	if device == "" {
		return nil, "smart check requires a device"
	}
	preds, err := parseLevelPreds(entry, SmartPredFields)
	if err != nil {
		return nil, "smart check: " + err.Error()
	}
	return smartCheck{base: b, runner: runner, device: device, preds: preds}, ""
}

// buildRaidCheck builds a Linux md software-RAID health check.
func buildRaidCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	preds, err := parseLevelPreds(entry, RaidPredFields)
	if err != nil {
		return nil, "raid check: " + err.Error()
	}
	return &raidCheck{
		base:         b,
		preds:        preds,
		sampler:      deps.RaidSampler,
		array:        cfgval.String(entry[CheckKeyArray]),
		sysfsChanges: cfgval.Bool(entry[CheckKeySysfsChanges]),
	}, ""
}

func buildLVMCheck(b base, entry map[string]any, runner execx.Runner) (Check, string) {
	preds, err := parseLevelPreds(entry, LVMPredFields)
	if err != nil {
		return nil, "lvm check: " + err.Error()
	}
	vg := cfgval.String(entry[CheckKeyVolumeGroup])
	lv := cfgval.String(entry[CheckKeyLogicalVolume])
	if lv != "" && vg == "" {
		return nil, "lvm check logical_volume requires volume_group"
	}
	runner = execx.RunnerOrDefault(runner)
	return &lvmCheck{base: b, runner: runner, volumeGroup: vg, logicalVolume: lv, preds: preds}, ""
}

// buildEdacCheck builds an ECC memory-error (EDAC) check.
func buildEdacCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	preds, err := parseLevelPreds(entry, EdacPredFields)
	if err != nil {
		return nil, "edac check: " + err.Error()
	}
	return edacCheck{base: b, preds: preds, sampler: deps.EdacSampler}, ""
}

// buildFdsCheck builds an open file-descriptors check.
func buildFdsCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	return buildLevelCheck(entry, FdsPredFields, "fds check", func(preds []levelPred) Check {
		return fdsCheck{base: b, preds: preds, sampler: deps.FdsSampler}
	})
}

// buildMemoryCheck builds a system RAM check.
func buildMemoryCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	return buildLevelCheck(entry, MemoryPredFields, "memory check", func(preds []levelPred) Check {
		return memoryCheck{base: b, preds: preds, sampler: deps.MemorySampler}
	})
}

// buildPidsCheck builds a kernel PID-table check.
func buildPidsCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	return buildLevelCheck(entry, PidsPredFields, "pids check", func(preds []levelPred) Check {
		return pidsCheck{base: b, preds: preds, sampler: deps.PidsSampler}
	})
}

// buildDiskIOCheck builds a block-device I/O rate check.
func buildDiskIOCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	device := cfgval.AsString(entry[CheckKeyDevice])
	if device == "" {
		return nil, "diskio check requires a device (e.g. sda, nvme0n1)"
	}
	preds, errs := requireLevelPreds(entry, DiskIOPredFields, "diskio check")
	if errs != "" {
		return nil, errs
	}
	return &diskIOCheck{base: b, device: device, preds: preds, sampler: deps.DiskIOSampler, state: &diskIOState{}}, ""
}

// buildPressureCheck builds a kernel PSI stall check.
func buildPressureCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	resource := cfgval.AsString(entry[CheckKeyResource])
	switch resource {
	case PressureResourceCPU, PressureResourceMemory, PressureResourceIO:
	default:
		return nil, "pressure check requires resource: " + PressureResourceSummary
	}
	preds, errs := requireLevelPreds(entry, PressurePredFields, "pressure check")
	if errs != "" {
		return nil, errs
	}
	return pressureCheck{base: b, resource: resource, preds: preds, sampler: deps.PressureSampler}, ""
}

// buildConntrackCheck builds a netfilter conntrack-table check.
func buildConntrackCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	return buildLevelCheck(entry, ConntrackPredFields, "conntrack check", func(preds []levelPred) Check {
		return conntrackCheck{base: b, preds: preds, sampler: deps.ConntrackSampler}
	})
}

// buildEntropyCheck builds an available-entropy check.
func buildEntropyCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	return buildSingleLevelCheck(entry, EntropyPredFields, "entropy check", func(pred levelPred) Check {
		return entropyCheck{base: b, op: pred.op, value: pred.value, sampler: deps.EntropySampler}
	})
}

// buildZombieCheck builds a zombie-process count check.
func buildZombieCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	return buildSingleLevelCheck(entry, ZombiePredFields, "zombies check", func(pred levelPred) Check {
		return zombieCheck{base: b, op: pred.op, value: pred.value, sampler: deps.ZombieSampler}
	})
}

// buildOomCheck builds an OOM-kill delta check (defaults to firing on any kill).
func buildOomCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	// delta is optional; the default fires on any OOM kill (> 0).
	op, value := cfgval.CompareOpGreater, 0.0
	if raw, present := entry[CheckKeyDelta]; present {
		var errs string
		if op, value, errs = parseDeltaThreshold(raw, "oom"); errs != "" {
			return nil, errs
		}
	}
	return &oomCheck{base: b, op: op, value: value, sampler: deps.OomSampler}, ""
}

// buildSwapCheck builds a swap usage or io check.
func buildSwapCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	metric := cfgval.AsString(entry[CheckKeyMetric])
	c := &swapCheck{base: b, metric: metric, sampler: deps.SwapSampler}
	switch metric {
	case SwapMetricUsage:
		preds, errs := requireLevelPreds(entry, SwapUsageFields, "swap usage")
		if errs != "" {
			return nil, errs
		}
		c.preds = preds
	case SwapMetricIO:
		op, v, errs := parseDeltaThreshold(entry[CheckKeyDelta], "swap io")
		if errs != "" {
			return nil, errs
		}
		c.op, c.value = op, v
	default:
		return nil, "swap check metric must be " + SwapMetricSummary
	}
	return c, ""
}
