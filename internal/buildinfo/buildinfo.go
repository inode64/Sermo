// Package buildinfo reports the binary's version, VCS revision and build date.
//
// Values come from the Go module build info embedded by `go build` (module
// version and, for a VCS checkout, the commit revision and time). The Version
// variable can also be set at link time for release builds:
//
//	go build -ldflags "-X sermo/internal/buildinfo.Version=1.2.0" ./cmd/sermod
package buildinfo

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// Version is the release version. Empty by default; set via -ldflags for a
// tagged build, otherwise the module version from build info is used.
var Version = ""

// resolve returns the display version plus the (short) VCS revision and build
// date, applying the same fallbacks used by both String and Short.
func resolve() (version, revision, date string) {
	version = Version
	if bi, ok := debug.ReadBuildInfo(); ok {
		if version == "" {
			version = bi.Main.Version
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				revision = s.Value
			case "vcs.time":
				date = s.Value
			}
		}
	}
	if version == "" || version == "(devel)" {
		version = "dev"
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	return version, revision, date
}

// Short returns a concise version string for compact display (e.g. a web
// footer): "1.2.0 (a1b2c3d4e5f6)", or just "dev" when no revision is embedded.
func Short() string {
	version, revision, _ := resolve()
	if revision != "" {
		return version + " (" + revision + ")"
	}
	return version
}

// String returns a multi-line, human-readable version banner, e.g.:
//
//	sermo 1.2.0 (a1b2c3d4e5f6, 2026-06-08T10:00:00Z)
//	  go1.26.3, linux/amd64
func String() string {
	version, revision, date := resolve()

	var meta []string
	if revision != "" {
		meta = append(meta, revision)
	}
	if date != "" {
		meta = append(meta, date)
	}
	line := "sermo " + version
	if len(meta) > 0 {
		line += " (" + strings.Join(meta, ", ") + ")"
	}
	return line + "\n  " + runtime.Version() + ", " + runtime.GOOS + "/" + runtime.GOARCH
}
