package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestValidateIncludeHiddenForRecursiveChecks(t *testing.T) {
	valid := validateIncludeHidden(t, map[string]any{
		"checks": map[string]any{
			"count-files": map[string]any{
				"type": "count", "path": "/var/lib/app", "of": "file", "recursive": true,
				"include_hidden": true, "op": ">", "value": 10,
			},
			"size-tree": map[string]any{
				"type": "size", "path": "/var/lib/app", "include_hidden": true,
				"grow_by": "1G", "within": "1h",
			},
		},
	})
	if len(valid) != 0 {
		t.Fatalf("valid include_hidden checks: %v", valid)
	}

	invalid := validateIncludeHidden(t, map[string]any{
		"checks": map[string]any{
			"count-files": map[string]any{
				"type": "count", "path": "/var/lib/app", "include_hidden": "yes", "op": ">", "value": 10,
			},
			"size-tree": map[string]any{
				"type": "size", "path": "/var/lib/app", "include_hidden": "yes",
				"grow_by": "1G", "within": "1h",
			},
		},
	})
	joined := strings.Join(invalid, "\n")
	for _, want := range []string{
		"checks.count-files count include_hidden must be a boolean",
		"checks.size-tree.include_hidden must be a boolean",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing issue %q in %s", want, joined)
		}
	}
}

func validateIncludeHidden(t *testing.T, tree map[string]any) []string {
	t.Helper()
	var issues []string
	validateCheckSection(tree, "checks", "/run/sermo/locks", func(format string, args ...any) {
		issues = append(issues, fmt.Sprintf(format, args...))
	})
	return issues
}
