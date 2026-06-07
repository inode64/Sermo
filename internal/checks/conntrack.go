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

// conntrackPred is one threshold predicate on a computed conntrack field.
type conntrackPred struct {
	field string // used_pct | free | count
	op    string
	value float64
}

// conntrackCheck watches the netfilter conntrack table against its maximum. Like
// disk it is a level check: OK==true means every predicate holds. A full table
// drops new connections (and logs "nf_conntrack: table full"), so catching it
// approaching the limit is valuable on busy gateways/proxies.
type conntrackCheck struct {
	base
	preds   []conntrackPred
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

	values := map[string]float64{"count": float64(s.Count)}
	var usedPct float64
	if s.Max > 0 {
		usedPct = float64(s.Count) / float64(s.Max) * 100
		values["used_pct"] = usedPct
		values["free"] = float64(s.Max - s.Count)
	}

	ok := true
	for _, p := range c.preds {
		v, known := values[p.field]
		if !known || !compareFloat(v, p.op, p.value) {
			ok = false
		}
	}

	res := c.result(ok, fmt.Sprintf("conntrack %d/%d entries (%.1f%%)", s.Count, s.Max, usedPct), start)
	res.Data = map[string]any{"count": s.Count, "max": s.Max, "used_pct": usedPct}
	if s.Max > 0 {
		res.Data["free"] = s.Max - s.Count
	}
	res.Data["value"] = usedPct
	if len(c.preds) > 0 {
		if v, ok := values[c.preds[0].field]; ok {
			res.Data["value"] = v
		}
	}
	return res
}

// defaultConntrackSampler reads the conntrack count and max from
// /proc/sys/net/netfilter (present when the nf_conntrack module is loaded).
func defaultConntrackSampler() (ConntrackSample, error) {
	count, err := readProcUint("/proc/sys/net/netfilter/nf_conntrack_count")
	if err != nil {
		return ConntrackSample{}, err
	}
	max, err := readProcUint("/proc/sys/net/netfilter/nf_conntrack_max")
	if err != nil {
		return ConntrackSample{}, err
	}
	return ConntrackSample{Count: count, Max: max}, nil
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

// parseConntrackPreds reads the used_pct/free/count predicates of a conntrack
// check. At least one is required; each is {op, value}.
func parseConntrackPreds(entry map[string]any) ([]conntrackPred, error) {
	var preds []conntrackPred
	for _, field := range []string{"used_pct", "free", "count"} {
		raw, ok := entry[field]
		if !ok {
			continue
		}
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s must be a mapping {op, value}", field)
		}
		op := asString(m["op"])
		if !validDiskOp(op) {
			return nil, fmt.Errorf("%s has invalid op %q", field, op)
		}
		val, err := strconv.ParseFloat(scalarString(m["value"]), 64)
		if err != nil {
			return nil, fmt.Errorf("%s value %q is not numeric", field, scalarString(m["value"]))
		}
		preds = append(preds, conntrackPred{field: field, op: op, value: val})
	}
	if len(preds) == 0 {
		return nil, fmt.Errorf("requires at least one of used_pct/free/count")
	}
	return preds, nil
}
