package checks

import (
	"context"
	"fmt"
	"time"
)

// EntropySamplerFunc reads the available entropy in bits, reporting ok = false
// when it cannot be read. Injected for tests; the default reads
// /proc/sys/kernel/random/entropy_avail.
type EntropySamplerFunc func() (uint64, bool)

// entropyCheck watches the kernel entropy pool against a threshold (typically
// `avail < N`). Low entropy makes reads from /dev/random block and slows crypto
// and TLS handshakes — most visible on VMs and headless/embedded hosts. Like
// storage it is a level check: OK==true means the threshold holds.
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
	res.Data = map[string]any{"avail": avail, fieldValue: avail}
	return res
}

// SampleEntropy returns one live kernel entropy observation using the default
// /proc/sys/kernel/random/entropy_avail reader. ok is false when unavailable.
func SampleEntropy() (avail uint64, ok bool) { return defaultEntropySampler() }

// defaultEntropySampler reads /proc/sys/kernel/random/entropy_avail.
func defaultEntropySampler() (uint64, bool) {
	n, err := readProcUint("/proc/sys/kernel/random/entropy_avail")
	if err != nil {
		return 0, false
	}
	return n, true
}
