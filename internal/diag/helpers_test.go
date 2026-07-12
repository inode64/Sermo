package diag

import (
	"strings"
	"testing"
	"time"
)

// builder.sort orders findings by severity (error < warning < info) and then by
// scope. Pin the exact order so a flipped comparison (sort by scope first, or
// reversed severity/scope) is caught.
func TestBuilderSort(t *testing.T) {
	b := &builder{}
	b.addf(LevelWarning, "alpha", "w") // rank 1
	b.addf(LevelError, "zeta", "e1")   // rank 0
	b.addf(LevelError, "beta", "e2")   // rank 0
	b.addf(LevelInfo, "gamma", "i")    // rank 2
	b.sort()

	want := []Finding{
		{Level: LevelError, Scope: "beta"},
		{Level: LevelError, Scope: "zeta"},
		{Level: LevelWarning, Scope: "alpha"},
		{Level: LevelInfo, Scope: "gamma"},
	}
	if len(b.findings) != len(want) {
		t.Fatalf("findings len = %d, want %d", len(b.findings), len(want))
	}
	for i, w := range want {
		if b.findings[i].Level != w.Level || b.findings[i].Scope != w.Scope {
			t.Errorf("findings[%d] = %s/%s, want %s/%s", i, b.findings[i].Level, b.findings[i].Scope, w.Level, w.Scope)
		}
	}
}

// checkAlignment warns when a check interval is below the resolution or not an
// exact multiple of it, and stays silent on an exact multiple or a non-positive
// resolution (the div-by-zero guard).
func TestCheckAlignment(t *testing.T) {
	const res = 30 * time.Second

	exact := &builder{}
	checkAlignment(exact, "s", 60*time.Second, res)
	if len(exact.findings) != 0 {
		t.Errorf("exact multiple: got %d findings, want 0", len(exact.findings))
	}

	below := &builder{}
	checkAlignment(below, "s", 10*time.Second, res)
	if len(below.findings) != 1 || !strings.Contains(below.findings[0].Message, "below") {
		t.Errorf("below resolution: got %+v, want one 'below' warning", below.findings)
	}

	notMultiple := &builder{}
	checkAlignment(notMultiple, "s", 40*time.Second, res) // rounds to 1*30s = 30s
	if len(notMultiple.findings) != 1 ||
		!strings.Contains(notMultiple.findings[0].Message, "not a multiple") ||
		!strings.Contains(notMultiple.findings[0].Message, "every 30s") {
		t.Errorf("not a multiple: got %+v, want 'not a multiple ... every 30s'", notMultiple.findings)
	}

	zeroRes := &builder{}
	checkAlignment(zeroRes, "s", 60*time.Second, 0)
	if len(zeroRes.findings) != 0 {
		t.Errorf("non-positive resolution: got %d findings, want 0 (guarded)", len(zeroRes.findings))
	}
}
