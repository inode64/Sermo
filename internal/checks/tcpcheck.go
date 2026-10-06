package checks

import (
	"context"
	"fmt"

	"sermo/internal/conn"
	"sermo/internal/netutil"
)

// tcpCheck dials a TCP host:port, optionally egressing through one
// or more interfaces (ifaces); ifaceAll requires every one to succeed.
type tcpCheck struct {
	base
	host     string
	ifaces   []string
	ifaceAll bool
	port     int
}

func (c tcpCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	start := run.start

	addr := netutil.JoinHostPort(c.host, c.port)
	chosen, perIface, err := tryInterfaces(c.ifaces, c.ifaceAll, func(iface string) error {
		nc, e := conn.BindDialer(iface).DialContext(ctx, conn.TransportTCP, addr)
		if e == nil {
			_ = nc.Close()
			return nil
		}
		return fmt.Errorf("dial %s: %w", addr, e)
	})
	if err != nil {
		r := c.unavailableResult(err.Error(), start)
		r.Data = c.resultData(perIface)
		return r
	}
	r := c.result(true, "connected to "+addr+ifaceSuffix(chosen), start)
	r.Data = c.resultData(perIface)
	r.Data[DataKeyLatencyMS] = r.Latency.Milliseconds()
	r.Data[DataKeySummary] = "Connected" + ifaceSuffix(chosen)
	return r
}

// resultData keeps the attempted target available even when dialing fails.
func (c tcpCheck) resultData(perIface map[string]any) map[string]any {
	data := map[string]any{DataKeyHost: c.host, DataKeyPort: c.port, DataKeyProtocol: conn.TransportTCP}
	if perIface != nil {
		data[DataKeyInterfaces] = perIface
	}
	return data
}
