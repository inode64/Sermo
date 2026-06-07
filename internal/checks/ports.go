package checks

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
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
// cycles (stateful, so it works built once as a host watch).
type portsCheck struct {
	base
	host           string
	ports          []int
	expect         string // open | closed | any
	match          string // all | any | none
	onChange       bool
	connectTimeout time.Duration

	primed bool
	last   map[int]bool // port -> open
}

func (c *portsCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	states := c.scan(ctx)
	if err := ctx.Err(); err != nil {
		return c.result(false, fmt.Sprintf("%s: scan timed out: %v", c.host, err), start)
	}
	matchHolds := c.evaluateMatch(states)

	var changes []string
	if c.onChange {
		if c.primed {
			for _, p := range c.ports {
				if was, ok := c.last[p]; ok && was != states[p] {
					changes = append(changes, fmt.Sprintf("%d %s->%s", p, portState(was), portState(states[p])))
				}
			}
		}
		c.primed = true
		c.last = states
	}

	open, closed := 0, 0
	for _, p := range c.ports {
		if states[p] {
			open++
		} else {
			closed++
		}
	}

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
		"host":    c.host,
		"total":   len(c.ports),
		"open":    open,
		"closed":  closed,
		"value":   open,
		"changed": strings.Join(changes, ","),
	}
	return res
}

// evaluateMatch applies the quantifier `match` over the ports whose state equals
// `expect`. expect=any always holds (only change detection matters then).
func (c *portsCheck) evaluateMatch(states map[int]bool) bool {
	if c.expect == "any" {
		return true
	}
	want := c.expect == "open"
	matched := 0
	for _, p := range c.ports {
		if states[p] == want {
			matched++
		}
	}
	switch c.match {
	case "any":
		return matched >= 1
	case "none":
		return matched == 0
	default: // all
		return matched == len(c.ports)
	}
}

// scan probes every port concurrently and returns port -> open. Unprobed ports
// are false (closed). When the check timeout fires, scan stops launching new
// probes; in-flight dials cancel promptly via the shared context.
func (c *portsCheck) scan(ctx context.Context) map[int]bool {
	out := make(map[int]bool, len(c.ports))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, portsScanConcurrency)

	for _, p := range c.ports {
		if err := ctx.Err(); err != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(port int) {
			defer wg.Done()
			defer func() { <-sem }()
			open := c.probe(ctx, port)
			mu.Lock()
			out[port] = open
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	return out
}

// probe reports whether a single TCP port accepts a connection.
func (c *portsCheck) probe(ctx context.Context, port int) bool {
	timeout := c.connectTimeout
	if timeout <= 0 {
		timeout = defaultPortConnectTimeout
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(pctx, "tcp", net.JoinHostPort(c.host, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func portState(open bool) string {
	if open {
		return "open"
	}
	return "closed"
}

// parsePortSpec parses a ports specification of comma-separated single ports and
// inclusive ranges, e.g. "80,443,1024-4000". Ports are de-duplicated and sorted;
// each must be 1..65535 and a range must be ascending.
func parsePortSpec(spec string) ([]int, error) {
	seen := map[int]bool{}
	for _, tok := range strings.Split(spec, ",") {
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
		if start < 1 || end > 65535 || start > end {
			return nil, fmt.Errorf("port range %q is out of 1..65535", tok)
		}
		for p := start; p <= end; p++ {
			seen[p] = true
		}
		if len(seen) > maxPorts {
			return nil, fmt.Errorf("too many ports (max %d)", maxPorts)
		}
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("no ports specified")
	}
	out := make([]int, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Ints(out)
	return out, nil
}
