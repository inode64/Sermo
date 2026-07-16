package checks

import (
	"testing"
)

func fakeConntrack(s ConntrackSample) ConntrackSamplerFunc {
	return func() (ConntrackSample, error) { return s, nil }
}

func conntrackWith(count, limit uint64) func(preds []levelPred) Check {
	return func(preds []levelPred) Check {
		return conntrackCheck{base: base{name: "c"}, preds: preds, sampler: fakeConntrack(ConntrackSample{Count: count, Max: limit})}
	}
}

func TestConntrackUsedPct(t *testing.T) {
	assertUsedPctWindow(t, conntrackWith(92000, 100000), 90, 95) // 92%
}

func TestConntrackFreeAbsolute(t *testing.T) {
	assertFreeFires(t, conntrackWith(95000, 100000)([]levelPred{{"free", "<", 10000}}), 5000)
}

func TestConntrackFreeClampsWhenCountExceedsMax(t *testing.T) {
	// The kernel lets count momentarily exceed nf_conntrack_max under bursts;
	// free must clamp to 0, not underflow the unsigned subtraction.
	assertFreeClamps(t, conntrackWith(110000, 100000)(nil))
}

func TestConntrackUnknownMaxNeverFires(t *testing.T) {
	assertUnknownMaxNeverFires(t, conntrackWith(95000, 0)([]levelPred{{"free", "<", 10000}}))
}

func TestBuildConntrackCheck(t *testing.T) {
	assertBuildLimitCheck(t, "conntrack",
		Deps{ConntrackSampler: fakeConntrack(ConntrackSample{Count: 85000, Max: 100000})})
}
