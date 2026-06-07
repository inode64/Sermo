package checks

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// FdsSample is one observation of the system-wide open file descriptors: the
// number currently allocated and the kernel maximum (fs.file-max).
type FdsSample struct {
	Allocated uint64
	Max       uint64
}

// FdsSamplerFunc reads the current fd sample. Injected for tests; the default
// reads /proc/sys/fs/file-nr.
type FdsSamplerFunc func() (FdsSample, error)

// fdsPred is one threshold predicate on a computed fd field.
type fdsPred struct {
	field string // used_pct | free | allocated
	op    string
	value float64
}

// fdsCheck watches the system-wide open file descriptors against the kernel
// maximum (fs.file-max). Like disk it is a level check: OK==true means every
// predicate holds. Catches fd exhaustion, which makes every open()/socket()/
// accept() across the host fail with EMFILE/ENFILE.
type fdsCheck struct {
	base
	preds   []fdsPred
	sampler FdsSamplerFunc
}

func (c fdsCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultFdsSampler
	}
	s, err := sampler()
	if err != nil {
		return c.result(false, "fds: "+err.Error(), start)
	}

	values := map[string]float64{"allocated": float64(s.Allocated)}
	// used_pct/free need the limit; an unknown limit leaves them "unknown" so a
	// predicate on them cannot hold (the level check is an AND).
	var usedPct float64
	if s.Max > 0 {
		usedPct = float64(s.Allocated) / float64(s.Max) * 100
		values["used_pct"] = usedPct
		values["free"] = float64(s.Max - s.Allocated)
	}

	ok := true
	for _, p := range c.preds {
		v, known := values[p.field]
		if !known || !compareFloat(v, p.op, p.value) {
			ok = false
		}
	}

	res := c.result(ok, fmt.Sprintf("fds %d/%d allocated (%.1f%%)", s.Allocated, s.Max, usedPct), start)
	res.Data = map[string]any{"allocated": s.Allocated, "max": s.Max, "used_pct": usedPct}
	if s.Max > 0 {
		res.Data["free"] = s.Max - s.Allocated
	}
	res.Data["value"] = usedPct
	if len(c.preds) > 0 {
		if v, ok := values[c.preds[0].field]; ok {
			res.Data["value"] = v
		}
	}
	return res
}

// defaultFdsSampler reads allocated (field 1) and max (field 3) from
// /proc/sys/fs/file-nr. The middle field (free handles) is always 0 on modern
// kernels, so allocated is the in-use count.
func defaultFdsSampler() (FdsSample, error) {
	data, err := os.ReadFile("/proc/sys/fs/file-nr")
	if err != nil {
		return FdsSample{}, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return FdsSample{}, fmt.Errorf("malformed /proc/sys/fs/file-nr")
	}
	alloc, e1 := strconv.ParseUint(fields[0], 10, 64)
	max, e3 := strconv.ParseUint(fields[2], 10, 64)
	if e1 != nil || e3 != nil {
		return FdsSample{}, fmt.Errorf("malformed /proc/sys/fs/file-nr")
	}
	return FdsSample{Allocated: alloc, Max: max}, nil
}

// parseFdsPreds reads the used_pct/free/allocated predicates of an fds check. At
// least one is required; each is {op, value}.
func parseFdsPreds(entry map[string]any) ([]fdsPred, error) {
	var preds []fdsPred
	for _, field := range []string{"used_pct", "free", "allocated"} {
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
		preds = append(preds, fdsPred{field: field, op: op, value: val})
	}
	if len(preds) == 0 {
		return nil, fmt.Errorf("requires at least one of used_pct/free/allocated")
	}
	return preds, nil
}
