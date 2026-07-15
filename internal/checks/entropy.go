package checks

import (
	"context"
	"fmt"
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
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultEntropySampler
	}
	return runThresholdCheck(c.base, c.op, c.value, sampler, "entropy: entropy_avail unavailable",
		func(avail uint64) string { return fmt.Sprintf("entropy_avail %d bits", avail) }, DataKeyAvail)
}

// SampleEntropy returns one live kernel entropy observation using the default
// /proc/sys/kernel/random/entropy_avail reader. ok is false when unavailable.
func SampleEntropy() (avail uint64, ok bool) { return defaultEntropySampler() }

// defaultEntropySampler reads /proc/sys/kernel/random/entropy_avail.
func defaultEntropySampler() (uint64, bool) {
	n, err := readProcUint(procEntropyAvailPath)
	if err != nil {
		return 0, false
	}
	return n, true
}
