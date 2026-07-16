package checks

import (
	"testing"
)

func fakeFds(s FdsSample) FdsSamplerFunc {
	return func() (FdsSample, error) { return s, nil }
}

func fdsWith(allocated, limit uint64) func(preds []levelPred) Check {
	return func(preds []levelPred) Check {
		return fdsCheck{base: base{name: "f"}, preds: preds, sampler: fakeFds(FdsSample{Allocated: allocated, Max: limit})}
	}
}

func TestFdsUsedPct(t *testing.T) {
	assertUsedPctWindow(t, fdsWith(9000, 10000), 90, 95) // 9000/10000 = 90%
}

func TestFdsFreeAbsolute(t *testing.T) {
	assertFreeFires(t, fdsWith(9000, 10000)([]levelPred{{"free", "<", 2000}}), 1000)
}

func TestFdsUnknownMaxNeverFires(t *testing.T) {
	// Max == 0 leaves used_pct/free unknown, so the predicate cannot hold.
	assertUnknownMaxNeverFires(t, fdsWith(9000, 0)([]levelPred{{"free", "<", 2000}}))
}

func TestFdsFreeClampsWhenAllocatedExceedsMax(t *testing.T) {
	// allocated > max (a transient or misreported sample) must clamp free to 0,
	// not underflow the unsigned subtraction into a huge bogus value.
	assertFreeClamps(t, fdsWith(12000, 10000)(nil))
}

func TestBuildFdsCheck(t *testing.T) {
	assertBuildLimitCheck(t, "fds",
		Deps{FdsSampler: fakeFds(FdsSample{Allocated: 8500, Max: 10000})})
}
