package checks

import (
	"context"

	"sermo/internal/cfgval"
	"sermo/internal/servicemgr"
)

// idleGateCheck leaves a socket-activated service asleep. While a unit it names
// only waits for demand the probe does not run at all, because connecting to
// that unit's socket would start it; `requires` cannot do this, since its gate
// judges results after the cycle's probes ran. The skipped result keeps the
// observation out of health, alerts, rules and remediation.
type idleGateCheck struct {
	Check
	units []string
	idle  servicemgr.UnitIdleFunc
}

func withIdleGate(check Check, entry map[string]any, idle servicemgr.UnitIdleFunc) Check {
	units := cfgval.StringList(entry[CheckKeySkipWhenIdle])
	if len(units) == 0 || idle == nil {
		return check
	}
	return idleGateCheck{Check: check, units: units, idle: idle}
}

func (c idleGateCheck) resultMetadata() Result { return checkResultMetadata(c.Check) }

func (c idleGateCheck) Run(ctx context.Context) Result {
	for _, unit := range c.units {
		if c.idle(ctx, unit) {
			res := checkResultMetadata(c.Check)
			res.OK, res.Skipped = true, true
			res.Message = "skipped: " + unit + " is idle, waiting for its socket, timer or path unit"
			return res
		}
	}
	return c.Check.Run(ctx)
}
