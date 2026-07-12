package dockerctl

import (
	"maps"
	"strings"
	"testing"
)

func dockerControl(extra map[string]any) map[string]any {
	c := map[string]any{ControlKeyType: ControlType}
	maps.Copy(c, extra)
	return map[string]any{sectionControl: c}
}

// SpecFromTree validates a docker control block. Pin the blank-host guard and
// the port range boundaries (1..65535), which mutation testing left unasserted.
func TestSpecFromTreeValidation(t *testing.T) {
	if _, _, err := SpecFromTree(dockerControl(map[string]any{ControlKeyHost: "   "})); err == nil ||
		!strings.Contains(err.Error(), "must not be blank") {
		t.Errorf("whitespace-only host: got %v, want 'must not be blank'", err)
	}

	for _, pc := range []struct {
		port    int
		wantErr bool
	}{
		{0, true},      // below range
		{1, false},     // lower boundary
		{65535, false}, // upper boundary
		{65536, true},  // above range
	} {
		_, _, err := SpecFromTree(dockerControl(map[string]any{ControlKeyHost: "tcp.example", ControlKeyPort: pc.port, ControlKeyContainer: "web"}))
		if (err != nil) != pc.wantErr {
			t.Errorf("port %d: err = %v, wantErr = %v", pc.port, err, pc.wantErr)
		}
	}
}
