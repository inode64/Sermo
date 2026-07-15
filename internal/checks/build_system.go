package checks

import (
	"sermo/internal/cfgval"
	"sermo/internal/execx"
)

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
	preds, errs := requireLevelPreds(entry, UsersPredFields, "users check")
	if errs != "" {
		return nil, errs
	}
	return usersCheck{base: b, preds: preds, sampler: deps.UsersSampler}, ""
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
	if runner == nil {
		runner = execx.CommandRunner{}
	}
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
	preds, errs := requireLevelPreds(entry, FdsPredFields, "fds check")
	if errs != "" {
		return nil, errs
	}
	return fdsCheck{base: b, preds: preds, sampler: deps.FdsSampler}, ""
}

// buildMemoryCheck builds a system RAM check.
func buildMemoryCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	preds, errs := requireLevelPreds(entry, MemoryPredFields, "memory check")
	if errs != "" {
		return nil, errs
	}
	return memoryCheck{base: b, preds: preds, sampler: deps.MemorySampler}, ""
}

// buildPidsCheck builds a kernel PID-table check.
func buildPidsCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	preds, errs := requireLevelPreds(entry, PidsPredFields, "pids check")
	if errs != "" {
		return nil, errs
	}
	return pidsCheck{base: b, preds: preds, sampler: deps.PidsSampler}, ""
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
	preds, errs := requireLevelPreds(entry, ConntrackPredFields, "conntrack check")
	if errs != "" {
		return nil, errs
	}
	return conntrackCheck{base: b, preds: preds, sampler: deps.ConntrackSampler}, ""
}

// buildEntropyCheck builds an available-entropy check.
func buildEntropyCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	preds, errs := requireLevelPreds(entry, EntropyPredFields, "entropy check")
	if errs != "" {
		return nil, errs
	}
	return entropyCheck{base: b, op: preds[0].op, value: preds[0].value, sampler: deps.EntropySampler}, ""
}

// buildZombieCheck builds a zombie-process count check.
func buildZombieCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	preds, errs := requireLevelPreds(entry, ZombiePredFields, "zombies check")
	if errs != "" {
		return nil, errs
	}
	return zombieCheck{base: b, op: preds[0].op, value: preds[0].value, sampler: deps.ZombieSampler}, ""
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
