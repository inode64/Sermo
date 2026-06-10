package checks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadEDAC(t *testing.T) {
	root := t.TempDir()
	for i, c := range []struct{ ce, ue string }{{"3", "0"}, {"5", "1"}} {
		mc := filepath.Join(root, "mc", "mc"+string(rune('0'+i)))
		if err := os.MkdirAll(mc, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(mc, "ce_count"), c.ce+"\n")
		writeFile(t, filepath.Join(mc, "ue_count"), c.ue+"\n")
	}
	st, err := readEDAC(root)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Present || st.CE != 8 || st.UE != 1 {
		t.Fatalf("edac = %+v, want present CE 8 UE 1", st)
	}

	// No controllers -> not present.
	if st, _ := readEDAC(t.TempDir()); st.Present {
		t.Error("a tree with no mc* must report not present")
	}
}

func edacWith(st edacCounts, preds ...edacPred) edacCheck {
	return edacCheck{base: base{name: "e", timeout: time.Second}, sampler: func() (edacCounts, error) { return st, nil }, preds: preds}
}

func TestEdacCheck(t *testing.T) {
	// Default: alert on any uncorrectable error.
	if res := edacWith(edacCounts{Present: true, CE: 2, UE: 1}).Run(context.Background()); !res.OK {
		t.Error("ue>0 should alert by default")
	}
	if res := edacWith(edacCounts{Present: true, CE: 2}).Run(context.Background()); res.OK {
		t.Error("only correctable errors must not alert by default")
	}
	// Predicate on correctable errors.
	if res := edacWith(edacCounts{Present: true, CE: 100}, edacPred{"ce", ">", 50}).Run(context.Background()); !res.OK {
		t.Error("ce>50 predicate should alert")
	}
	// EDAC unavailable -> failure (so a misconfigured target is noticed).
	if res := edacWith(edacCounts{Present: false}).Run(context.Background()); res.OK {
		t.Error("absent EDAC must fail the check")
	}
}
