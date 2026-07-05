package checks

import (
	"context"
	"fmt"
	"path/filepath"
	"time"
)

// EdacCounts is the aggregated EDAC memory-error count across controllers.
type EdacCounts struct {
	CE      int64 // correctable
	UE      int64 // uncorrectable
	Present bool  // false when the platform exposes no EDAC controllers
}

// EdacSamplerFunc reads the current EDAC counters. Injected for tests; the default
// reads /sys/devices/system/edac.
type EdacSamplerFunc func() (EdacCounts, error)

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

	values := map[string]float64{fieldCE: float64(st.CE), fieldUE: float64(st.UE)}
	ok := st.UE > 0 // default alert condition
	if len(c.preds) > 0 {
		ok = levelPredsHold(c.preds, values)
	}

	r := c.result(ok, fmt.Sprintf("edac: %d correctable, %d uncorrectable", st.CE, st.UE), start)
	r.Data = map[string]any{fieldCE: float64(st.CE), fieldUE: float64(st.UE)}
	return r
}

// SampleEdac returns one live EDAC memory-error observation using the default
// sysfs sampler.
func SampleEdac() (EdacCounts, error) { return defaultEdacSampler() }

// defaultEdacSampler reads /sys/devices/system/edac.
func defaultEdacSampler() (EdacCounts, error) { return readEDAC("/sys/devices/system/edac") }

// readEDAC sums ce_count/ue_count across the memory controllers under root.
func readEDAC(root string) (EdacCounts, error) {
	mcs, err := filepath.Glob(filepath.Join(root, "mc", "mc*"))
	if err != nil {
		return EdacCounts{}, err
	}
	if len(mcs) == 0 {
		return EdacCounts{}, nil
	}
	var st EdacCounts
	st.Present = true
	for _, mc := range mcs {
		ce, err := readEdacCounter(filepath.Join(mc, "ce_count"))
		if err != nil {
			return EdacCounts{}, err
		}
		ue, err := readEdacCounter(filepath.Join(mc, "ue_count"))
		if err != nil {
			return EdacCounts{}, err
		}
		st.CE += ce
		st.UE += ue
	}
	return st, nil
}

const maxEdacCounter = uint64(1<<63 - 1)

func readEdacCounter(path string) (int64, error) {
	n, err := readProcUint(path)
	if err != nil {
		return 0, err
	}
	if n > maxEdacCounter {
		return 0, fmt.Errorf("counter %s overflows int64", path)
	}
	return int64(n), nil
}

// parseEdacPreds reads the optional ce/ue predicates.
