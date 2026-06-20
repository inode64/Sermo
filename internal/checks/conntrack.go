package checks

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// ConntrackSample is one observation of the netfilter connection-tracking table:
// the entries currently tracked and the table maximum (nf_conntrack_max).
type ConntrackSample struct {
	Count uint64
	Max   uint64
}

// ConntrackSamplerFunc reads the current conntrack sample. Injected for tests;
// the default reads /proc/sys/net/netfilter/nf_conntrack_{count,max}.
type ConntrackSamplerFunc func() (ConntrackSample, error)

// conntrackCheck watches the netfilter conntrack table against its maximum. Like
// storage it is a level check: OK==true means every predicate holds. A full table
// drops new connections (and logs "nf_conntrack: table full"), so catching it
// approaching the limit is valuable on busy gateways/proxies.
type conntrackCheck struct {
	base
	preds   []levelPred
	sampler ConntrackSamplerFunc
}

func (c conntrackCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultConntrackSampler
	}
	s, err := sampler()
	if err != nil {
		return c.result(false, "conntrack: "+err.Error(), start)
	}
	return levelCountResult(c.base, c.preds, "conntrack", "entries", "count", s.Count, s.Max, start)
}

// SampleConntrack returns one live netfilter conntrack observation using the
// default /proc/sys/net/netfilter reader. Exposed so callers like the web
// backend can render a conntrack gauge without running a full conntrack check.
func SampleConntrack() (ConntrackSample, error) { return defaultConntrackSampler() }

// defaultConntrackSampler reads the conntrack count and max from
// /proc/sys/net/netfilter (present when the nf_conntrack module is loaded).
func defaultConntrackSampler() (ConntrackSample, error) {
	count, err := readProcUint("/proc/sys/net/netfilter/nf_conntrack_count")
	if err != nil {
		return ConntrackSample{}, err
	}
	maxConn, err := readProcUint("/proc/sys/net/netfilter/nf_conntrack_max")
	if err != nil {
		return ConntrackSample{}, err
	}
	return ConntrackSample{Count: count, Max: maxConn}, nil
}

// readProcUint reads a sysctl-style file holding a single unsigned integer.
func readProcUint(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("malformed %s", path)
	}
	return n, nil
}
