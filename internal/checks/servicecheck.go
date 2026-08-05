package checks

import (
	"context"
	"fmt"

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
	ctx, run := c.begin(ctx)
	defer run.close()
	start := run.start

	status, err := c.status(ctx)
	if err != nil {
		return c.result(false, fmt.Sprintf("status: %v", err), start)
	}
	ok := string(status) == c.expect
	result := c.result(ok, fmt.Sprintf("status %s (want %s)", status, c.expect), start)
	// Keep the normalized backend state with the monitoring sample. Consumers
	// such as the dashboard can then use the service worker's fresh observation
	// instead of retaining an older status-list cache entry after an external or
	// CLI operation.
	result.Data = map[string]any{DataKeyStatus: string(status)}
	return result
}
