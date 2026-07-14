package config

import (
	"reflect"
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

func TestCollapseOSBranchShapes(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  any
	}{
		{
			name: "map branch merges with siblings",
			input: map[string]any{
				"timeout": "5s",
				keyOS:     map[string]any{"gentoo": map[string]any{"url": "http://localhost/gentoo"}},
			},
			want: map[string]any{"timeout": "5s", "url": "http://localhost/gentoo"},
		},
		{
			name:  "scalar branch replaces selector-only value",
			input: map[string]any{keyOS: map[string]any{keyOSDefault: "/run/example.pid"}},
			want:  "/run/example.pid",
		},
		{
			name:  "scalar branch is ignored when siblings remain",
			input: map[string]any{"keep": true, keyOS: map[string]any{"gentoo": "/run/example.pid"}},
			want:  map[string]any{"keep": true},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := collapseOS(tc.input, "gentoo")
			if err != nil {
				t.Fatalf("collapseOS() error = %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("collapseOS() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestCollapseOSRejectsMergedMapThatResolvesToScalar(t *testing.T) {
	_, err := collapseOS(map[string]any{
		"keep": true,
		keyOS: map[string]any{
			"gentoo": map[string]any{keyOS: map[string]any{"gentoo": "/run/example.pid"}},
		},
	}, "gentoo")
	if err == nil || !strings.Contains(err.Error(), `os branch "gentoo" must resolve to a mapping when merged`) {
		t.Fatalf("collapseOS() error = %v", err)
	}
}
