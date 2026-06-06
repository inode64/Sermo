package config

import (
	"os"
	"strings"
)

// osMarker is the built-in ${os} reference, substituted with the detected OS id
// (os-release ID: gentoo, debian, ubuntu, ...).
const osMarker = "${os}"

// detectedOS holds the OS id used for ${os} and `os:` selectors. Resolved once at
// package load; tests may override it before calling Load.
var detectedOS = detectOS()

func detectOS() string {
	if v := strings.TrimSpace(os.Getenv("SERMO_OS")); v != "" {
		return strings.ToLower(v)
	}
	if id := osReleaseID(); id != "" {
		return id
	}
	return "linux"
}

// osReleaseID returns the lowercased ID= field of os-release, or "".
func osReleaseID() string {
	for _, path := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "ID="); ok {
				return strings.ToLower(strings.Trim(v, `"'`))
			}
		}
	}
	return ""
}

// bakeOS substitutes ${os} with the detected OS id across every loaded document,
// alongside bakeArch.
func (c *Config) bakeOS() {
	for _, doc := range c.docs {
		doc.Body = bindToken(doc.Body, osMarker, detectedOS).(map[string]any)
	}
}

// applyOSSelectors collapses every `os:` selector block in every loaded document.
// An `os:` key holding a map of os-id -> block selects the branch for the detected
// OS (or a `default` branch), merges it into the surrounding map, and discards the
// rest. It works at any depth — aliases, checks, processes, policy, ... — and runs
// at load, before resolution.
func (c *Config) applyOSSelectors() {
	for _, doc := range c.docs {
		doc.Body = collapseOS(doc.Body, detectedOS).(map[string]any)
	}
}

func collapseOS(v any, osID string) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		var selector map[string]any
		for k, e := range t {
			if k == "os" {
				if m, ok := e.(map[string]any); ok {
					selector = m
					continue
				}
			}
			out[k] = collapseOS(e, osID)
		}
		if selector != nil {
			if branch := selectOSBranch(selector, osID); branch != nil {
				out = mergeMaps(out, collapseOS(branch, osID).(map[string]any))
			}
		}
		return out
	case []any:
		for i := range t {
			t[i] = collapseOS(t[i], osID)
		}
		return t
	default:
		return t
	}
}

// selectOSBranch returns the block for osID, else a `default` block, else nil.
func selectOSBranch(selector map[string]any, osID string) map[string]any {
	if b, ok := selector[osID].(map[string]any); ok {
		return b
	}
	if b, ok := selector["default"].(map[string]any); ok {
		return b
	}
	return nil
}
