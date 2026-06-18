package app

import "sermo/internal/checks"

func checkSamplersFromDeps(deps Deps) checks.Samplers {
	return checks.Samplers{
		DiskUsage:            deps.DiskUsage,
		NetSampler:           deps.NetSampler,
		PingSampler:          deps.PingSampler,
		RouteSampler:         deps.RouteSampler,
		OomSampler:           deps.OomSampler,
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
