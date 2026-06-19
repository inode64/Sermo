package config

import (
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

// archMarker is the built-in ${arch} reference, substituted with the machine
// architecture (uname -m style: x86_64, aarch64, ...) so a daemon can locate an
// arch-specific binary or library, e.g. binary: /usr/bin/qemu-system-${arch}.
const archMarker = "${arch}"

// detectedArch holds the machine architecture used for ${arch}. It is resolved
// once at package load; tests may override it before calling Load.
var detectedArch = detectArch()

func detectArch() string {
	if v := envOverride("SERMO_ARCH"); v != "" {
		return v
	}
	// Native uname(2) via x/sys/unix — no external `uname` process.
	var u unix.Utsname
	if err := unix.Uname(&u); err == nil {
		if m := strings.TrimSpace(unix.ByteSliceToString(u.Machine[:])); m != "" {
			return m
		}
	}
	return goarchToUname(runtime.GOARCH)
}

// goarchToUname maps Go's GOARCH names to the uname -m names paths use, as a
// fallback when uname is unavailable.
func goarchToUname(goarch string) string {
	switch goarch {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	case "386":
		return "i686"
	default:
		return goarch
	}
}

// bakeBuiltins substitutes ${arch} and ${os} with their detected values across
// every loaded document in a single tree walk. Doing it once at load — before
// version-template discovery and before the variable pipeline — keeps the
// tokens out of variable values (so they never trip the no-nested-variables
// rule) and lets the version glob and library paths see concrete values.
func (c *Config) bakeBuiltins() {
	repl := strings.NewReplacer(archMarker, detectedArch, osMarker, detectedOS)
	for _, doc := range c.docs {
		doc.Body = bindTokens(doc.Body, repl).(map[string]any)
	}
	// The global document (defaults.variables, watches, …) lives in Global.Raw,
	// not c.docs. Bake there too so ${arch}/${os} work consistently everywhere
	// instead of surviving as literal tokens that later trip variable validation.
	if c.Global.Raw != nil {
		c.Global.Raw = bindTokens(c.Global.Raw, repl).(map[string]any)
		// collapseOS/bindTokens build fresh maps, so re-point the extracted
		// Defaults view (it aliased the pre-bake Raw["defaults"] sub-map).
		if defaults, ok := c.Global.Raw["defaults"].(map[string]any); ok {
			c.Global.Defaults = defaults
		}
	}
}
