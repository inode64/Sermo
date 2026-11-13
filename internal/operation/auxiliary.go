package operation

import (
	"context"
	"errors"
	"fmt"

	"sermo/internal/config"
	"sermo/internal/servicemgr"
)

func (e Engine) auxiliaryPresent(ctx context.Context, unit config.AuxiliaryUnit) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("query auxiliary unit: %w", err)
	}
	if !unit.Optional {
		return true, nil
	}
	probe, ok := e.Manager.(servicemgr.UnitPresence)
	if !ok {
		return false, errors.New("unit presence query is unavailable")
	}
	present, err := probe.UnitExists(ctx, unit.Unit)
	if err != nil {
		return false, fmt.Errorf("query auxiliary unit %s: %w", unit.Unit, err)
	}
	return present, nil
}

func (e Engine) startAuxiliaryUnits(ctx context.Context, result *Result, afterPrimary bool) bool {
	for _, unit := range e.Lifecycle.AuxiliaryUnits {
		if unit.StartAfterPrimary != afterPrimary {
			continue
		}
		present, err := e.auxiliaryPresent(ctx, unit)
		if err == nil && present {
			err = e.Manager.Start(ctx, unit.Unit)
		}
		if err != nil {
			return failPhase(ctx, result, "operation timed out starting also_service "+unit.Unit, "start "+unit.Unit+": ", err)
		}
	}
	return true
}
