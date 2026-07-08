package servicemgr

import (
	"context"
	"time"

	"sermo/internal/execx"
)

func runSystemctlShow(ctx context.Context, runner execx.Runner, timeout time.Duration, property, unit string) (execx.Result, error) {
	return execx.Run(ctx, runner, timeout, cmdSystemctl, systemctlCmdShow, systemctlFlagProperty, property, systemctlFlagValue, commandArgTerminator, unit)
}
