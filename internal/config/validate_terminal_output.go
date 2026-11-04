package config

import (
	"path/filepath"

	"sermo/internal/cfgval"
)

func validateWatchDrainOutputAction(prefix string, block, then map[string]any, allow bool, add addFunc) bool {
	raw, present := then[WatchThenKeyDrainOutput]
	if !present {
		return false
	}
	path := thenFieldPath(prefix, WatchThenKeyDrainOutput)
	m, ok := raw.(map[string]any)
	if !ok {
		add("%s must be a mapping", path)
		return false
	}
	if !allow {
		add("%s is only valid on a terminal_output watch", path)
	}
	for key := range m {
		if key != WatchDrainOutputKeyLogFile {
			add(validationNotSupportedFormat, path+"."+key)
		}
	}
	file := cfgval.String(m[WatchDrainOutputKeyLogFile])
	if !filepath.IsAbs(file) || filepath.Clean(file) != file {
		add("%s.log_file must be an absolute clean path", path)
	}
	requireWatchCooldown(prefix, path, block, "terminal output recovery must be paced", add)
	return true
}
