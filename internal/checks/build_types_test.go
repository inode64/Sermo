package checks

import (
	"strings"
	"testing"
	"time"
)

// Every advertised single-shot type must be handled by buildCheck: a bare
// `{type: X}` entry may fail its own field validation, but it must never come
// back as "unsupported type" — that would mean the exported list (which config
// validation trusts) and the builder dispatch drifted apart.
func TestSingleShotCheckTypesAreBuildable(t *testing.T) {
	for _, typ := range SingleShotCheckTypes {
		_, warns := Build(map[string]any{"probe": map[string]any{"type": typ}}, Deps{DefaultTimeout: time.Second})
		for _, w := range warns {
			if strings.Contains(w, "unsupported type") {
				t.Errorf("%s: not handled by buildCheck: %s", typ, w)
			}
		}
	}
}
