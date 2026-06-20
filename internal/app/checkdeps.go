package app

import "sermo/internal/checks"

func checkSamplersFromDeps(deps Deps) checks.Samplers {
	return checks.Samplers{
		StorageUsage:         deps.StorageUsage,
		NetSampler:           deps.NetSampler,
		PingSampler:          deps.PingSampler,
		SwapSampler:          deps.SwapSampler,
		RouteSampler:         deps.RouteSampler,
		LoadSampler:          deps.LoadSampler,
		OomSampler:           deps.OomSampler,
		FdsSampler:           deps.FdsSampler,
		MemorySampler:        deps.MemorySampler,
		PressureSampler:      deps.PressureSampler,
		PidsSampler:          deps.PidsSampler,
		DiskIOSampler:        deps.DiskIOSampler,
		SensorSampler:        deps.SensorSampler,
		RaidSampler:          deps.RaidSampler,
		EdacSampler:          deps.EdacSampler,
		MountSampler:         deps.MountSampler,
		ConntrackSampler:     deps.ConntrackSampler,
		FirewallRulesSampler: deps.FirewallRulesSampler,
		EntropySampler:       deps.EntropySampler,
		ZombieSampler:        deps.ZombieSampler,
	}
}

func checkDepsFromAppDeps(deps Deps, base checks.Deps) checks.Deps {
	return checkSamplersFromDeps(deps).ApplyTo(base)
}
