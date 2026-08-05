package checks

import (
	"context"
	"fmt"
	"time"

	"sermo/internal/metrics"
	"sermo/internal/procnet"
)

// tcpConnectionsCheck counts established TCP sockets on a local server port.
// It observes transport connections, not authenticated application users.
type tcpConnectionsCheck struct {
	base
	port  int
	preds []levelPred
	count func(int) (int, error)
}

func (c tcpConnectionsCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	start := run.start
	if err := ctx.Err(); err != nil {
		return c.unavailableResult(err, start)
	}

	counter := c.count
	if counter == nil {
		counter = procnet.CountTCPConnections
	}
	count, err := counter(c.port)
	if err != nil {
		return c.unavailableResult(err, start)
	}
	values := map[string]float64{DataKeyCount: float64(count)}
	res := c.result(levelPredsHold(c.preds, values), fmt.Sprintf("%d established TCP connection(s) on port %d", count, c.port), start)
	res.Data = map[string]any{
		DataKeyPort:  c.port,
		DataKeyCount: count,
		DataKeyValue: float64(count),
		DataKeyUnit:  metrics.MetricUnitConnections,
	}
	return res
}

func (c tcpConnectionsCheck) unavailableResult(err error, start time.Time) Result {
	res := c.base.unavailableResult(fmt.Sprintf("tcp connections on port %d: %v", c.port, err), start)
	res.Data = map[string]any{DataKeyPort: c.port, DataKeySampleError: err.Error()}
	return res
}
