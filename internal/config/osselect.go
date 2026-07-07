package config

import (
	"os"
	"strings"
)

// osMarker is the built-in ${os} reference, substituted with the detected OS id
// (os-release ID: gentoo, debian, ubuntu, ...).
const osMarker = "${os}"

const keyOSDefault = "default"

const (
	osReleaseEtcPath = "/etc/os-release"
	osReleaseUsrPath = "/usr/lib/os-release"
)

// detectedOS holds the OS id used for ${os} and `os:` selectors. Resolved once at
// package load; tests may override it before calling Load.
var detectedOS = detectOS()

func detectOS() string {
	if v := envOverride(envOSOverride); v != "" {
		return strings.ToLower(v)
	}
	if id := osReleaseID(); id != "" {
		return id
	}
	return "linux"
}

// osReleaseID returns the lowercased ID= field of os-release, or "".
func osReleaseID() string {
	for _, path := range []string{osReleaseEtcPath, osReleaseUsrPath} {
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

// applyOSSelectors collapses every `os:` selector block in every loaded document.
// An `os:` key holding a map of os-id -> block selects the branch for the detected
// OS (or a `default` branch), merges it into the surrounding map, and discards the
// rest. It works at any depth — service, checks, processes, policy, ... — and runs
// at load, before resolution.
func (c *Config) applyOSSelectors() {
	for _, doc := range c.docs {
		doc.Body = collapseOS(doc.Body, detectedOS).(map[string]any)
	}
	// The global document (defaults, watches, …) lives in Global.Raw, not c.docs,
	// so collapse os: selectors there too.
	if c.Global.Raw != nil {
		c.Global.Raw = collapseOS(c.Global.Raw, detectedOS).(map[string]any)
	}
}

func collapseOS(v any, osID string) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		var selector map[string]any
		for k, e := range t {
			if k == keyOS {
				if m, ok := e.(map[string]any); ok {
					selector = m
					continue
				}
			}
			out[k] = collapseOS(e, osID)
		}
		if selector != nil {
			if branch := selectOSBranch(selector, osID); branch != nil {
				if bm, ok := branch.(map[string]any); ok {
					out = mergeMaps(out, collapseOS(bm, osID).(map[string]any))
				} else if len(out) == 0 {
					// A list/scalar branch (e.g. os-specific pidfile path
					// candidates) replaces the value when `os:` is the only key.
					return collapseOS(branch, osID)
				}
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

// selectOSBranch returns the branch for osID, else a `default` branch, else nil.
// The branch may be a map (merged into the parent) or a list/scalar (which
// replaces the parent value when `os:` is the only key).
func selectOSBranch(selector map[string]any, osID string) any {
	if b, ok := selector[osID]; ok {
		return b
	}
	if b, ok := selector[keyOSDefault]; ok {
		return b
	}
	return nil
}
