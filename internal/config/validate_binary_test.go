package config

import (
	"fmt"
	"testing"
)

func TestValidateBinaryResolvedPath(t *testing.T) {
	assertNoWatchIssues(t, watchConfig("selection", map[string]any{"type": "binary", "path": "/usr/bin/php", "resolved_path": "/usr/lib/php/bin/php"}))
	for _, value := range []any{"", 3, "relative"} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			assertWatchIssues(t, watchConfig("selection", map[string]any{"type": "binary", "path": "/usr/bin/php", "resolved_path": value}), "watches.selection.check.resolved_path")
		})
	}
}
