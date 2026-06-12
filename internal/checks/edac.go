package checks

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"time"
)

// edacCounts is the aggregated EDAC memory-error count across controllers.
type edacCounts struct {
	CE      int64 // correctable
	UE      int64 // uncorrectable
	Present bool  // false when the platform exposes no EDAC controllers
}

// EdacSamplerFunc reads the current EDAC counters. Injected for tests; the default
// reads /sys/devices/system/edac.
type EdacSamplerFunc func() (edacCounts, error)

// edacCheck reports ECC memory errors from the kernel EDAC subsystem. `ce` is the
// cumulative correctable-error count and `ue` the uncorrectable count (a single
// uncorrectable error is serious). With no predicate it alerts when ue > 0;
// predicates on `ce`/`ue` override that. The counts are recorded over time so a
// rising correctable rate (failing DIMM) is visible. When the platform exposes no
// EDAC controllers the check fails (so a misconfigured target is noticed).
type edacCheck struct {
	base
	sampler EdacSamplerFunc
	preds   []levelPred
}

func (c edacCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultEdacSampler
	}
	st, err := sampler()
	if err != nil {
		return c.result(false, "edac: "+err.Error(), start)
	}
	if !st.Present {
		return c.result(false, "edac: no EDAC controllers (ECC reporting unavailable)", start)
	}

	values := map[string]float64{"ce": float64(st.CE), "ue": float64(st.UE)}
	ok := st.UE > 0 // default alert condition
	if len(c.preds) > 0 {
		ok = true
		for _, p := range c.preds {
			if !compareFloat(values[p.field], p.op, p.value) {
				ok = false
			}
		}
	}

	r := c.result(ok, fmt.Sprintf("edac: %d correctable, %d uncorrectable", st.CE, st.UE), start)
	r.Data = map[string]any{"ce": float64(st.CE), "ue": float64(st.UE)}
	return r
}

// defaultEdacSampler reads /sys/devices/system/edac.
func defaultEdacSampler() (edacCounts, error) { return readEDAC("/sys/devices/system/edac") }

// readEDAC sums ce_count/ue_count across the memory controllers under root.
func readEDAC(root string) (edacCounts, error) {
	mcs, err := filepath.Glob(filepath.Join(root, "mc", "mc*"))
	if err != nil {
		return edacCounts{}, err
	}
	if len(mcs) == 0 {
		return edacCounts{}, nil
	}
	var st edacCounts
	st.Present = true
	for _, mc := range mcs {
		st.CE += readInt(filepath.Join(mc, "ce_count"))
		st.UE += readInt(filepath.Join(mc, "ue_count"))
	}
	return st, nil
}

// readInt reads a sysfs counter file as an int64, returning 0 on error.
func readInt(path string) int64 {
	n, _ := strconv.ParseInt(readTrim(path), 10, 64)
	return n
}

// parseEdacPreds reads the optional ce/ue predicates.
