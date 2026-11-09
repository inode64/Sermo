package checks

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
	"sermo/internal/execx"
	"sermo/internal/netutil"
)

// maxPorts caps a single ports check to keep a scan bounded.
const maxPorts = 16384

// portsScanConcurrency bounds simultaneous dials within one ports check.
const portsScanConcurrency = 256

// defaultPortConnectTimeout is the per-port dial timeout when connect_timeout is
// unset. Closed (refused) ports answer instantly; filtered ports wait this long.
const defaultPortConnectTimeout = time.Second

// portsCheck probes a set of TCP ports on a host and evaluates a quantified state
// expectation, plus optional change detection. It is health-style: OK==true means
// the expectation holds (no problem). The ports are given as a list with ranges
// (e.g. "80,443,1024-4000"); expect is the per-port desired state (open|closed|
// any) and match the quantifier over the ports in that state (all=AND, any=OR,
// none=NOT). With on_change it also fails on any open<->closed transition between
// cycles while the check instance is reused by a service worker or host watch.
// A config reload/worker rebuild creates a fresh baseline.
type portsCheck struct {
	base
	host           string
	ifaces         []string
	ifaceAll       bool
	ports          []int
	expect         string // open | closed | any
	match          string // all | any | none
	onChange       bool
	connectTimeout time.Duration

	// dialFunc dials addr through iface honoring ctx. nil uses the real
	// interface-bound dialer; tests override it to make timing deterministic.
	dialFunc func(ctx context.Context, iface, addr string) (net.Conn, error)

	primed bool
	last   map[int]portObservation
}

func (c *portsCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	start := run.start

	states, unobserved := c.scan(ctx)
	if err := ctx.Err(); err != nil {
		return c.unavailableResult(fmt.Sprintf("%s: %s", c.host, execx.ContextFailure(err, c.timeout)), start)
	}
	// Neither a verdict nor a change: the on_change baseline stays put, so a
	// failed lookup cannot fake every port closing and opening again.
	if unobserved != nil {
		return c.unavailableResult(fmt.Sprintf("%s: %v", c.host, unobserved), start)
	}
	matchHolds := c.evaluateMatch(states)

	var changes []string
	if c.onChange {
		if c.primed {
			for _, p := range c.ports {
				if was, ok := c.last[p]; ok && was.open != states[p].open {
					changes = append(changes, fmt.Sprintf("%d %s->%s", p, portState(was.open), portState(states[p].open)))
				}
			}
		}
		c.primed = true
		c.last = states
	}

	open := 0
	for _, p := range c.ports {
		if states[p].open {
			open++
		}
	}
	closed := len(c.ports) - open

	ok := matchHolds && len(changes) == 0
	var msg string
	switch {
	case len(changes) > 0:
		msg = fmt.Sprintf("%s: port state changed: %s", c.host, strings.Join(changes, ", "))
	case !matchHolds:
		msg = fmt.Sprintf("%s: expected %s %s of %d ports; %d open, %d closed", c.host, c.match, c.expect, len(c.ports), open, closed)
	default:
		msg = fmt.Sprintf("%s: %d ports, %d open, %d closed (%s %s)", c.host, len(c.ports), open, closed, c.match, c.expect)
	}

	res := c.result(ok, msg, start)
	res.Data = map[string]any{
		DataKeyHost:     c.host,
		DataKeyTotal:    len(c.ports),
		PortStateOpen:   open,
		PortStateClosed: closed,
		DataKeyValue:    open,
		DataKeyChanged:  strings.Join(changes, ","),
	}
	return res
}

// evaluateMatch applies the quantifier `match` over the ports whose state equals
// `expect`. expect=any always holds (only change detection matters then).
func (c *portsCheck) evaluateMatch(states map[int]portObservation) bool {
	if c.expect == PortExpectAny {
		return true
	}
	matched := 0
	for _, p := range c.ports {
		if states[p].matched {
			matched++
		}
	}
	switch c.match {
	case PortMatchAny:
		return matched >= 1
	case PortMatchNone:
		return matched == 0
	default: // all
		return matched == len(c.ports)
	}
}

// scan probes every port concurrently and returns its observation, plus the
// first probe that observed nothing. When the check timeout fires, scan stops
// launching new probes; in-flight dials cancel promptly via the shared context.
func (c *portsCheck) scan(ctx context.Context) (map[int]portObservation, error) {
	out := make(map[int]portObservation, len(c.ports))
	var unobserved error
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, portsScanConcurrency)

	for _, p := range c.ports {
		if err := ctx.Err(); err != nil {
			break
		}
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			open, err := c.probe(ctx, p)
			mu.Lock()
			defer mu.Unlock()
			out[p] = open
			if err != nil && unobserved == nil {
				unobserved = fmt.Errorf("port %d not observed: %w", p, err)
			}
		})
	}
	wg.Wait()
	return out, unobserved
}

// portObservation separates exposure from an expectation over every path: a
// partially open port is exposed but satisfies neither all-open nor all-closed.
type portObservation struct {
	open    bool
	matched bool
}

// probe observes a single TCP port across the configured interfaces.
func (c *portsCheck) probe(ctx context.Context, port int) (portObservation, error) {
	timeout := c.connectTimeout
	if timeout <= 0 {
		timeout = defaultPortConnectTimeout
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dial := c.dialFunc
	if dial == nil {
		dial = func(ctx context.Context, iface, addr string) (net.Conn, error) {
			return conn.BindDialer(iface).DialContext(ctx, conn.TransportTCP, addr)
		}
	}
	var unobserved error
	opened, tried := 0, 0
	_, _, err := tryInterfaces(pctx, c.ifaces, c.ifaceAll, func(ctx context.Context, iface string) error {
		tried++
		nc, err := dial(ctx, iface, netutil.JoinHostPort(c.host, port))
		if err == nil {
			_ = nc.Close()
			opened++
			return nil
		}
		if !portClosedBy(err) {
			unobserved = err
		}
		// A refusal on one path cannot establish that the port is closed
		// on every path. Keep all-match walks going after closed/unseen paths.
		if c.ifaceAll {
			return nil
		}
		return err
	})
	observed := portObservation{open: opened > 0}
	switch c.expect {
	case PortStateOpen:
		observed.matched = observed.open && (!c.ifaceAll || opened == tried)
	case PortStateClosed:
		observed.matched = !observed.open
	default:
		observed.matched = true
	}
	if !c.ifaceAll && err == nil {
		return observed, nil
	}
	return observed, unobserved
}

// portClosedBy reports whether a failed dial is an answer about the port: the
// target or the network refused it, or nothing answered in time (a filtered
// port). Any other failure never got that far: an unresolved name, no route, an
// interface that could not be bound, a socket the host could not open.
func portClosedBy(err error) bool {
	if _, lookup := errors.AsType[*net.DNSError](err); lookup {
		return false
	}
	timeout, ok := errors.AsType[net.Error](err)
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EHOSTUNREACH) || (ok && timeout.Timeout())
}

func portState(open bool) string {
	if open {
		return PortStateOpen
	}
	return PortStateClosed
}

// ParsePortSpec parses a ports specification of comma-separated single ports and
// inclusive ranges, e.g. "80,443,1024-4000". Ports are de-duplicated and sorted;
// each must be a valid TCP port and a range must be ascending.
func ParsePortSpec(spec string) ([]int, error) {
	seen := map[int]bool{}
	for tok := range strings.SplitSeq(spec, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(tok, "-")
		start, err := strconv.Atoi(strings.TrimSpace(lo))
		if err != nil {
			return nil, fmt.Errorf("invalid port %q", tok)
		}
		end := start
		if isRange {
			end, err = strconv.Atoi(strings.TrimSpace(hi))
			if err != nil {
				return nil, fmt.Errorf("invalid port range %q", tok)
			}
		}
		if !cfgval.ValidTCPPort(start) || !cfgval.ValidTCPPort(end) || start > end {
			return nil, fmt.Errorf("port range %q is out of %s", tok, cfgval.TCPPortRange())
		}
		for p := start; p <= end; p++ {
			seen[p] = true
		}
		if len(seen) > maxPorts {
			return nil, fmt.Errorf("too many ports (max %d)", maxPorts)
		}
	}
	if len(seen) == 0 {
		return nil, errors.New("no ports specified")
	}
	return slices.Sorted(maps.Keys(seen)), nil
}
