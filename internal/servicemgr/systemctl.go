package servicemgr

import (
	"context"
	"fmt"
	"time"

	"sermo/internal/execx"
)

func runSystemctlShow(ctx context.Context, runner execx.Runner, timeout time.Duration, property, unit string) (execx.Result, error) {
	res, err := execx.Run(ctx, runner, timeout, cmdSystemctl, systemctlCmdShow, systemctlFlagProperty, property, systemctlFlagValue, commandArgTerminator, unit)
	if err != nil {
		return res, fmt.Errorf("systemctl show %s for %s: %w", property, unit, err)
	}
	return res, nil
}
