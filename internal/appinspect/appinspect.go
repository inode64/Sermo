// Package appinspect probes the applications described by catalog daemons: it
// stats their binary and runs their version command to report installation,
// version and health. It is the single source of this logic shared by the
// sermoctl `apps`/`libs`/`services` listings and the web dashboard, so both
// surfaces report identically.
package appinspect

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/execx"
)

// probeTimeout bounds each version command invocation.
const probeTimeout = 5 * time.Second

// Report is one application's installed/version/health summary.
type Report struct {
	Name         string `json:"name"`
	DisplayName  string `json:"display_name"`
	Binary       string `json:"binary"`
	Permissions  string `json:"permissions,omitempty"`
	Version      string `json:"version"`
	VersionShort string `json:"version_short"`
	Installed    bool   `json:"installed"`
	OK           bool   `json:"ok"`
	Status       string `json:"status"`
}

// List inspects every catalog daemon in the category. When includeMissing is
// false only installed applications (binary present) are returned. The order
// follows config.DaemonsInCategory, which sorts by name.
func List(ctx context.Context, runner execx.Runner, cfg *config.Config, category string, includeMissing bool) []Report {
	if cfg == nil {
		return nil
	}
	var reports []Report
	for _, name := range cfg.DaemonsInCategory(category) {
		resolved, _ := cfg.ResolveCatalog(category, name)
		r := Inspect(ctx, runner, name, resolved)
		if !r.Installed && !includeMissing {
			continue
		}
		reports = append(reports, r)
	}
	return reports
}

// Inspect probes a single resolved daemon: it stats the binary and, when
// present, runs the version command to capture the version and confirm it runs.
func Inspect(ctx context.Context, runner execx.Runner, name string, resolved config.Resolved) Report {
	r := Report{
		Name:        name,
		DisplayName: config.DisplayName(resolved.Tree, name),
		Binary:      binaryPath(resolved.Tree),
	}

	switch info, err := os.Stat(r.Binary); {
	case r.Binary == "":
		r.Status = "no binary configured"
		return r
	case err != nil:
		r.Status = "not installed"
		return r
	case info.IsDir():
		r.Permissions = modeString(info)
		r.Status = "error: " + r.Binary + " is a directory"
		return r
	case info.Mode().Perm()&0o111 == 0:
		r.Permissions = modeString(info)
		r.Installed = true
		r.Status = "error: " + r.Binary + " is not executable"
		return r
	default:
		r.Permissions = modeString(info)
	}
	r.Installed = true

	vc := versionCommandFor(resolved.Tree, "version")
	if len(vc.argv) == 0 {
		r.OK = true
		r.Status = "ok"
		return r
	}

	res, err := execx.Run(ctx, runner, probeTimeout, vc.argv[0], vc.argv[1:]...)
	switch {
	case err != nil && res.ExitCode == 0:
		r.Status = "error: " + err.Error()
	case res.ExitCode != vc.expectExit:
		r.Status = fmt.Sprintf("error: exit %d (want %d)", res.ExitCode, vc.expectExit)
		if line := firstNonEmptyLine(res.Stderr); line != "" {
			r.Status += ": " + line
		}
	default:
		if ok, detail := vc.stdout.Match(res.Stdout); !ok {
			r.Status = "error: stdout " + detail
		} else if ok, detail := vc.stderr.Match(res.Stderr); !ok {
			r.Status = "error: stderr " + detail
		} else {
			r.OK = true
			r.Status = "ok"
			r.Version = firstNonEmptyLine(res.Stdout)
			if r.Version == "" {
				r.Version = firstNonEmptyLine(res.Stderr)
			}
			r.VersionShort = shortVersionFor(ctx, runner, resolved.Tree, r.Version)
		}
	}
	return r
}

// modeString renders the binary's permissions like `ls -l` does, with the octal
// form appended (e.g. "-rwxr-xr-x (0755)").
func modeString(info os.FileInfo) string {
	return fmt.Sprintf("%s (%#o)", info.Mode(), info.Mode().Perm())
}

// shortVersionFor resolves the app's short version. When the daemon configures a
// `version_short` command that prints the bare version, its first non-empty
// output line is trusted verbatim — no regex, sidestepping parsing ambiguity.
// Otherwise (no such command, or it errors or prints nothing) it falls back to
// parsing the raw version line with ShortVersion.
func shortVersionFor(ctx context.Context, runner execx.Runner, tree map[string]any, rawVersion string) string {
	if vc := versionCommandFor(tree, "version_short"); len(vc.argv) > 0 {
		res, err := execx.Run(ctx, runner, probeTimeout, vc.argv[0], vc.argv[1:]...)
		if err == nil || res.ExitCode != 0 {
			if line := firstNonEmptyLine(res.Stdout); line != "" {
				return line
			}
			if line := firstNonEmptyLine(res.Stderr); line != "" {
				return line
			}
		}
	}
	return ShortVersion(rawVersion)
}

// binaryPath returns the resolved binary path of a daemon: its preflight
// `binary` check path when present, otherwise the `binary` variable.
func binaryPath(tree map[string]any) string {
	if pf, ok := tree["preflight"].(map[string]any); ok {
		if bin, ok := pf["binary"].(map[string]any); ok {
			if p := cfgval.AsString(bin["path"]); p != "" {
				return p
			}
		}
	}
	if vars, ok := tree["variables"].(map[string]any); ok {
		return cfgval.AsString(vars["binary"])
	}
	return ""
}

// versionCommand is a daemon's resolved version command and the expectations its
// result must meet: the exit code and optional stdout/stderr matchers.
type versionCommand struct {
	argv       []string
	expectExit int
	stdout     checks.OutputMatcher
	stderr     checks.OutputMatcher
}

// versionCommandFor returns a daemon's version command and outcome expectations
// for the named entry (`version` or `version_short`), looked up in
// `preflight.<key>` then `commands.<key>`. argv is nil when no such command is
// configured.
func versionCommandFor(tree map[string]any, key string) versionCommand {
	for _, src := range []string{"preflight", "commands"} {
		section, ok := tree[src].(map[string]any)
		if !ok {
			continue
		}
		entry, ok := section[key].(map[string]any)
		if !ok {
			continue
		}
		argv := cfgval.StringList(entry["command"])
		if len(argv) == 0 {
			continue
		}
		vc := versionCommand{argv: argv}
		if v, ok := cfgval.Int(entry["expect_exit"]); ok {
			vc.expectExit = v
		}
		vc.stdout, _ = checks.ParseOutputMatcher(entry["expect_stdout"])
		vc.stderr, _ = checks.ParseOutputMatcher(entry["expect_stderr"])
		return vc
	}
	return versionCommand{}
}

// shortVersionRE matches the first dotted numeric version in a raw version line:
// a `major.minor` with an optional `.patch`. By stopping at three components it
// excludes any further build numbers (e.g. the `.1` in `2.8.4.1`) and trailing
// suffixes (`p18`, `-P1`, `(2)`, `-MariaDB`), which keeps only the version and
// at most the patchlevel. Requiring at least `major.minor` avoids latching onto
// stray single digits (e.g. the `5` in perl's "perl 5, version 42 ... (v5.42.0)").
var shortVersionRE = regexp.MustCompile(`[0-9]+\.[0-9]+(?:\.[0-9]+)?`)

// ShortVersion reduces a raw version line (as captured in Report.Version) to
// just its numeric version, keeping at most three components
// (major.minor.patch). It returns the first dotted numeric token found, or ""
// when the line carries no recognizable version.
func ShortVersion(s string) string {
	return shortVersionRE.FindString(s)
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
