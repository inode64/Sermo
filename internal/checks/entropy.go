package checks

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// EntropySamplerFunc reads the available entropy in bits, reporting ok = false
// when it cannot be read. Injected for tests; the default reads
// /proc/sys/kernel/random/entropy_avail.
type EntropySamplerFunc func() (uint64, bool)

// entropyCheck watches the kernel entropy pool against a threshold (typically
// `avail < N`). Low entropy makes reads from /dev/random block and slows crypto
// and TLS handshakes — most visible on VMs and headless/embedded hosts. Like
// disk it is a level check: OK==true means the threshold holds.
type entropyCheck struct {
	base
	op      string
	value   float64
	sampler EntropySamplerFunc
}

func (c entropyCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultEntropySampler
	}
	avail, ok := sampler()
	if !ok {
		return c.result(false, "entropy: entropy_avail unavailable", start)
	}
	met := compareFloat(float64(avail), c.op, c.value)
	res := c.result(met, fmt.Sprintf("entropy_avail %d bits", avail), start)
	res.Data = map[string]any{"avail": avail, "value": avail}
	return res
}

// defaultEntropySampler reads /proc/sys/kernel/random/entropy_avail.
func defaultEntropySampler() (uint64, bool) {
	n, err := readProcUint("/proc/sys/kernel/random/entropy_avail")
	if err != nil {
		return 0, false
	}
	return n, true
}

// parseEntropyThreshold reads the required avail {op, value} of an entropy check.
func parseEntropyThreshold(entry map[string]any) (op string, value float64, err error) {
	m, ok := entry["avail"].(map[string]any)
	if !ok {
		return "", 0, fmt.Errorf("requires avail {op, value}")
	}
	op = asString(m["op"])
	if !validDiskOp(op) {
		return "", 0, fmt.Errorf("avail has invalid op %q", op)
	}
	value, perr := strconv.ParseFloat(scalarString(m["value"]), 64)
	if perr != nil {
		return "", 0, fmt.Errorf("avail value %q is not numeric", scalarString(m["value"]))
	}
	return op, value, nil
}
