package checks

import (
	"context"
	"fmt"
	"time"

	"sermo/internal/servicemgr"
)

// serviceCheck compares the service's backend status to an expected value
// . The status function is injected so the check stays single-shot.
type serviceCheck struct {
	base
	expect string
	status func(context.Context) (servicemgr.Status, error)
}

func (c serviceCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	status, err := c.status(ctx)
	if err != nil {
		return c.result(false, fmt.Sprintf("status: %v", err), start)
	}
	ok := string(status) == c.expect
	return c.result(ok, fmt.Sprintf("status %s (want %s)", status, c.expect), start)
}
