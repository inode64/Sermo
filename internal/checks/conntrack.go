package checks

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	procConntrackCountPath = "/proc/sys/net/netfilter/nf_conntrack_count"
	procConntrackMaxPath   = "/proc/sys/net/netfilter/nf_conntrack_max"
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
	return runSampledLevelCount(c.base, c.preds, samplerOr(c.sampler, defaultConntrackSampler),
		func(s ConntrackSample) (uint64, uint64) { return s.Count, s.Max },
		"conntrack", "entries", DataKeyCount)
}

// defaultConntrackSampler reads the conntrack count and max from
// /proc/sys/net/netfilter (present when the nf_conntrack module is loaded).
func defaultConntrackSampler() (ConntrackSample, error) {
	count, err := readProcUint(procConntrackCountPath)
	if err != nil {
		return ConntrackSample{}, err
	}
	maxConn, err := readProcUint(procConntrackMaxPath)
	if err != nil {
		return ConntrackSample{}, err
	}
	return ConntrackSample{Count: count, Max: maxConn}, nil
}

// readProcUint reads a sysctl-style file holding a single unsigned integer.
func readProcUint(path string) (uint64, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is a fixed sysctl/proc counter file
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(data)), numericBaseDecimal, numericBits64)
	if err != nil {
		return 0, fmt.Errorf(malformedFileFormat, path)
	}
	return n, nil
}
