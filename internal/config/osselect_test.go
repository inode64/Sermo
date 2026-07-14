package config

import (
	"strings"
	"testing"
)

func TestApplyOSSelectorsRejectsDocumentScalar(t *testing.T) {
	cfg := &Config{Global: Global{Raw: map[string]any{
		keyOS: map[string]any{keyOSDefault: "/run/example.pid"},
	}}}

	err := cfg.applyOSSelectors()
	if err == nil || !strings.Contains(err.Error(), "global config: document must resolve to a mapping") {
		t.Fatalf("applyOSSelectors() error = %v", err)
	}
}
