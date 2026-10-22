package operation

import (
	"context"
	"errors"
	"slices"
	"testing"

	"sermo/internal/config"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

// The forecast must name exactly the actions a real attempt on the active
// service refuses or fails for process identity, with the status and message
// it records; every other action must not be forecast.
func TestForecastActiveMatchesTheEngine(t *testing.T) {
	t.Parallel()
	live := []process.Process{{PID: 100, StartTicks: 10, Exe: "/usr/bin/alloy", ExeOK: true, Source: process.SourceBackend}}
	identityErrors := []string{errProcessIdentity.Error(), errReplacedBinary.Error(), errProcessAbsent.Error()}
	tests := []struct {
		name     string
		mode     config.ServiceProcessMode
		identity process.PrincipalIdentity
		reload   bool
		want     []string
	}{
		{name: "daemon runs as another user", mode: config.ServiceProcessResident, identity: process.PrincipalIdentity{Attributed: true, Required: true}, reload: true, want: []string{actionRestart, actionStart, actionReload, actionResume}},
		{name: "another user without reload support", mode: config.ServiceProcessResident, identity: process.PrincipalIdentity{Attributed: true, Required: true}, want: []string{actionRestart, actionStart, actionResume}},
		{name: "replaced binary", mode: config.ServiceProcessResident, identity: process.PrincipalIdentity{Attributed: true, Required: true, ReplacedExecutable: true}, reload: true, want: []string{actionStart, actionReload, actionResume}},
		{name: "trusted daemon", mode: config.ServiceProcessResident, identity: process.PrincipalIdentity{Attributed: true, Required: true, Trusted: true}, reload: true},
		{name: "no strict selector", mode: config.ServiceProcessResident, identity: process.PrincipalIdentity{Attributed: true}, reload: true},
		{name: "process-free service", mode: config.ServiceProcessNone, identity: process.PrincipalIdentity{Attributed: true, Required: true}, reload: true},
	}
	// prepareRepair refuses an active service before anything else.
	refuseActiveRepair := func(context.Context) ([]string, error) {
		return nil, errors.New("service is active; repair requires failed or inactive state")
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			build := func() Engine {
				engine := defaultHarness().engine()
				engine.Lifecycle.ProcessMode = tt.mode
				engine.RepairStalePIDFiles = refuseActiveRepair
				if !tt.reload {
					engine.ReloadFunc = nil
				}
				procs := live
				if !tt.identity.Attributed {
					procs = nil
				}
				engine.ObserveProcesses = func() (process.Observation, error) {
					return process.Observation{Processes: procs, IdentityRequired: tt.identity.Required, Trusted: tt.identity.Trusted, ReplacedExecutable: tt.identity.ReplacedExecutable, AbsenceKnown: true}, nil
				}
				return engine
			}
			forecasts := build().ForecastActive(servicemgr.StatusActive, tt.identity)
			var got []string
			for _, f := range forecasts {
				got = append(got, f.Action)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("forecast actions = %v, want %v (%+v)", got, tt.want, forecasts)
			}
			for _, action := range ServiceActions() {
				res := build().Do(context.Background(), action)
				i := slices.IndexFunc(forecasts, func(f Forecast) bool { return f.Action == action })
				if i < 0 {
					if slices.Contains(identityErrors, res.Message) {
						t.Fatalf("%s: engine recorded %s %q without a forecast", action, res.Status, res.Message)
					}
					continue
				}
				if forecasts[i].Status != res.Status || forecasts[i].Message != res.Message {
					t.Fatalf("%s: forecast %+v, engine recorded %s %q", action, forecasts[i], res.Status, res.Message)
				}
			}
		})
	}
}

// Only an active service reaches the identity rule.
func TestForecastActiveNeedsAnActiveService(t *testing.T) {
	t.Parallel()
	engine := defaultHarness().engine()
	engine.Lifecycle.ProcessMode = config.ServiceProcessResident
	unverified := process.PrincipalIdentity{Attributed: true, Required: true}
	for _, status := range []servicemgr.Status{servicemgr.StatusInactive, servicemgr.StatusFailed} {
		if forecast := engine.ForecastActive(status, unverified); forecast != nil {
			t.Fatalf("%s service forecast %+v", status, forecast)
		}
	}
}
