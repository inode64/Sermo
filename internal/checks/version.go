package checks

import "sermo/internal/cfgval"

// VersionCommandEntry returns the resolved version-command entry for key
// ("version" or "version_short"): preflight.<key> takes precedence over
// commands.<key>, and an entry only counts when it carries a non-empty command
// argv. Shared by the apps listings (appinspect) and the version.on_change
// monitor (app) so the precedence rule lives in one place. nil when neither
// section declares one.
func VersionCommandEntry(tree map[string]any, key string) map[string]any {
	for _, src := range []string{"preflight", "commands"} {
		if section, ok := tree[src].(map[string]any); ok {
			if entry, ok := section[key].(map[string]any); ok && len(cfgval.StringList(entry["command"])) > 0 {
				return entry
			}
		}
	}
	return nil
}
