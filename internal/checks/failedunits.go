package checks

import (
	"context"
	"fmt"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/execx"
	"sermo/internal/servicemgr"
)

// FailedUnitsSample is one observation of the init units in a failed state.
type FailedUnitsSample struct {
	Backend servicemgr.Backend
	Units   []string
}

// FailedUnitsSamplerFunc lists the init units the host reports as failed. The
// backend is auto, systemd or openrc, already parsed by the builder. Injected
// for tests; the default asks the detected init backend through the configured
// execx runner.
type FailedUnitsSamplerFunc func(ctx context.Context, backend servicemgr.Backend, runner execx.Runner, timeout time.Duration) (FailedUnitsSample, error)

// failedUnitsCheck counts the init units in a failed state and names them.
//
// It is the only view Sermo has of a unit with no catalog profile: service
// monitoring covers configured services, so a site-local backup job that fails
// every night is otherwise invisible. Condition-style — the `count` predicate
// (default `> 0`) decides — and deliberately without any remediation action:
// restarting an arbitrary unit Sermo knows nothing about is not a safe action.
type failedUnitsCheck struct {
	base
	backend servicemgr.Backend
	op      string
	value   float64
	runner  execx.Runner
	sampler FailedUnitsSamplerFunc
}

func (c failedUnitsCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	start := run.start

	sampler := c.sampler
	if sampler == nil {
		sampler = defaultFailedUnitsSampler
	}
	sample, err := sampler(ctx, c.backend, c.runner, c.timeout)
	if err != nil {
		return c.unavailableResult(CheckTypeFailedUnits+": "+execx.FormatContextOrError(err, c.timeout), start)
	}
	count := uint64(len(sample.Units))
	// Name them: which unit failed is what tells the operator what broke, and a
	// count alone would send them back to the host to find out. The same list
	// serves the message and the dashboard reading.
	named := strings.Join(sample.Units, ", ")
	// Both branches key off the count, never off the joined text: a sampler that
	// ever yields an unnamed unit must still report one failed unit rather than
	// none.
	message := "no failed units"
	if count > 0 {
		message = fmt.Sprintf("%d failed unit(s): %s", count, named)
	}
	res := c.result(compareFloat(float64(count), c.op, c.value), message, start)
	res.Data = map[string]any{
		DataKeyBackend: string(sample.Backend),
		DataKeyCount:   count,
		DataKeyValue:   count,
	}
	if count > 0 {
		res.Data[DataKeyUnits] = named
	}
	return res
}

func buildFailedUnitsCheck(b base, entry map[string]any, runner execx.Runner, deps Deps) (Check, string) {
	backend, err := servicemgr.ParseBackend(cfgval.AsString(entry[CheckKeyBackend]))
	if err != nil {
		return nil, CheckTypeFailedUnits + " check backend must be " + servicemgr.BackendInitSummary
	}
	// count is optional; the default fires on any failed unit (> 0).
	op, value := cfgval.CompareOpGreater, 0.0
	if _, present := entry[CheckKeyCount]; present {
		pred, errs := requireSingleLevelPred(entry, FailedUnitsPredFields, CheckTypeFailedUnits+" check")
		if errs != "" {
			return nil, errs
		}
		op, value = pred.op, pred.value
	}
	return failedUnitsCheck{
		base:    b,
		backend: backend,
		op:      op,
		value:   value,
		runner:  runner,
		sampler: deps.FailedUnitsSampler,
	}, ""
}

// defaultFailedUnitsSampler lists the failed units of the requested backend,
// detecting it when the check asks for auto. A generated configuration names the
// host's real backend, so the detection cost is paid only by a hand-written
// check that leaves it at the default.
func defaultFailedUnitsSampler(ctx context.Context, backend servicemgr.Backend, runner execx.Runner, timeout time.Duration) (FailedUnitsSample, error) {
	runner = execx.RunnerOrDefault(runner)
	if backend == servicemgr.BackendAuto {
		detection, err := servicemgr.Detector{Runner: runner, Timeout: timeout}.Detect(ctx, servicemgr.BackendAuto)
		if err != nil {
			return FailedUnitsSample{}, fmt.Errorf("detect backend: %w", err)
		}
		backend = detection.Backend
	}
	units, err := servicemgr.ListFailedUnits(ctx, backend, runner, timeout)
	if err != nil {
		return FailedUnitsSample{}, fmt.Errorf("list units: %w", err)
	}
	return FailedUnitsSample{Backend: backend, Units: units}, nil
}
