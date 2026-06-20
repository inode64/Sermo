package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/execx"
)

func TestCheckDepsFromAppDepsCopiesSamplers(t *testing.T) {
	deps := Deps{
		StorageUsage:     func(string) (checks.StorageStats, error) { return checks.StorageStats{}, nil },
		NetSampler:       func(string) (checks.NetSample, error) { return checks.NetSample{}, nil },
		PingSampler:      func(string, string, int, time.Duration) (checks.PingSample, error) { return checks.PingSample{}, nil },
		SwapSampler:      func() (checks.SwapSample, error) { return checks.SwapSample{}, nil },
		RouteSampler:     func(string) ([]checks.DefaultRoute, error) { return nil, nil },
		LoadSampler:      func() (checks.LoadSample, error) { return checks.LoadSample{}, nil },
		OomSampler:       func() (uint64, bool) { return 0, true },
		FdsSampler:       func() (checks.FdsSample, error) { return checks.FdsSample{}, nil },
		MemorySampler:    func() (checks.MemorySample, error) { return checks.MemorySample{}, nil },
		PressureSampler:  func(string) (checks.PressureSample, error) { return checks.PressureSample{}, nil },
		PidsSampler:      func() (checks.PidsSample, error) { return checks.PidsSample{}, nil },
		DiskIOSampler:    func(string) (checks.DiskIOSample, error) { return checks.DiskIOSample{}, nil },
		SensorSampler:    func() ([]checks.SensorReading, error) { return nil, nil },
		RaidSampler:      func() (checks.RaidStatus, error) { return checks.RaidStatus{}, nil },
		EdacSampler:      func() (checks.EdacCounts, error) { return checks.EdacCounts{}, nil },
		MountSampler:     func() ([]checks.Mount, error) { return nil, nil },
		ConntrackSampler: func() (checks.ConntrackSample, error) { return checks.ConntrackSample{}, nil },
		FirewallRulesSampler: func(context.Context, string, execx.Runner) (checks.FirewallRulesSample, error) {
			return checks.FirewallRulesSample{}, nil
		},
		EntropySampler: func() (uint64, bool) { return 0, true },
		ZombieSampler:  func() (uint64, bool) { return 0, true },
	}
	got := checkDepsFromAppDeps(deps, checks.Deps{Service: "web", DefaultTimeout: time.Second})
	if got.Service != "web" || got.DefaultTimeout != time.Second {
		t.Fatalf("base deps not preserved: %+v", got)
	}
	samplers := map[string]bool{
		"StorageUsage":         got.StorageUsage != nil,
		"NetSampler":           got.NetSampler != nil,
		"PingSampler":          got.PingSampler != nil,
		"SwapSampler":          got.SwapSampler != nil,
		"RouteSampler":         got.RouteSampler != nil,
		"LoadSampler":          got.LoadSampler != nil,
		"OomSampler":           got.OomSampler != nil,
		"FdsSampler":           got.FdsSampler != nil,
		"MemorySampler":        got.MemorySampler != nil,
		"PressureSampler":      got.PressureSampler != nil,
		"PidsSampler":          got.PidsSampler != nil,
		"DiskIOSampler":        got.DiskIOSampler != nil,
		"SensorSampler":        got.SensorSampler != nil,
		"RaidSampler":          got.RaidSampler != nil,
		"EdacSampler":          got.EdacSampler != nil,
		"MountSampler":         got.MountSampler != nil,
		"ConntrackSampler":     got.ConntrackSampler != nil,
		"FirewallRulesSampler": got.FirewallRulesSampler != nil,
		"EntropySampler":       got.EntropySampler != nil,
		"ZombieSampler":        got.ZombieSampler != nil,
	}
	for name, ok := range samplers {
		if !ok {
			t.Fatalf("%s was not copied", name)
		}
	}
}
