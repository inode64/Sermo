package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// osMarker is the built-in ${os} reference, substituted with the detected OS id
// (os-release ID: gentoo, debian, ubuntu, ...).
const osMarker = "${os}"

const keyOSDefault = SelectionKeywordDefault

const (
	osReleaseEtcPath = "/etc/os-release"
	osReleaseUsrPath = "/usr/lib/os-release"
	osReleaseIDKey   = "ID="
	osReleaseTrimSet = `"'`
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

// OSReleasePaths returns the os-release files checked in priority order.
func OSReleasePaths() []string {
	return []string{osReleaseEtcPath, osReleaseUsrPath}
}

// osReleaseID returns the lowercased ID= field of os-release, or "".
func osReleaseID() string {
	for _, path := range OSReleasePaths() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for line := range strings.SplitSeq(string(data), configLineSeparator) {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), osReleaseIDKey); ok {
				return strings.ToLower(strings.Trim(v, osReleaseTrimSet))
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
func (c *Config) applyOSSelectors() error {
	for _, doc := range c.docs {
		body, err := collapseOS(doc.Body, detectedOS)
		if err != nil {
			return fmt.Errorf("collapse os selector in %s: %w", doc.Path, err)
		}
		selected, ok := body.(map[string]any)
		if !ok {
			return fmt.Errorf("collapse os selector in %s: document must resolve to a mapping", doc.Path)
		}
		doc.Body = selected
	}
	// The global document (defaults, watches, …) lives in Global.Raw, not c.docs,
	// so collapse os: selectors there too.
	if c.Global.Raw != nil {
		raw, err := collapseOS(c.Global.Raw, detectedOS)
		if err != nil {
			return fmt.Errorf("collapse os selector in global config: %w", err)
		}
		selected, ok := raw.(map[string]any)
		if !ok {
			return errors.New("collapse os selector in global config: document must resolve to a mapping")
		}
		c.Global.Raw = selected
	}
	return nil
}

func collapseOS(v any, osID string) (any, error) {
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
			collapsed, err := collapseOS(e, osID)
			if err != nil {
				return nil, err
			}
			out[k] = collapsed
		}
		if selector != nil {
			if branch := selectOSBranch(selector, osID); branch != nil {
				if bm, ok := branch.(map[string]any); ok {
					collapsed, err := collapseOS(bm, osID)
					if err != nil {
						return nil, err
					}
					selected, ok := collapsed.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("os branch %q must resolve to a mapping when merged", osID)
					}
					out = mergeMaps(out, selected)
				} else if len(out) == 0 {
					// A list/scalar branch (e.g. os-specific pidfile path
					// candidates) replaces the value when `os:` is the only key.
					return collapseOS(branch, osID)
				}
			}
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, value := range t {
			collapsed, err := collapseOS(value, osID)
			if err != nil {
				return nil, err
			}
			out[i] = collapsed
		}
		return out, nil
	default:
		return t, nil
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
