// Package appinspect probes the applications described by catalog daemons: it
// stats their binary, runs their health command when present, and reports
// installation, version and health. It is the single source of this logic shared
// by the sermoctl `apps`/`libs`/`services` listings and the web dashboard, so
// both surfaces report identically.
package appinspect

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"regexp"
	"syscall"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/execx"
)

// probeTimeout bounds each app probe command invocation.
const probeTimeout = 5 * time.Second

// Report is one application's installed/version/health summary.
type Report struct {
	Name         string `json:"name"`
	DisplayName  string `json:"display_name"`
	Category     string `json:"category,omitempty"`
	Binary       string `json:"binary"`
	Permissions  string `json:"permissions,omitempty"`
	User         string `json:"user,omitempty"`
	Group        string `json:"group,omitempty"`
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

// Inspect probes a single resolved daemon: it stats the binary, runs health to
// confirm it runs when configured, and captures the version when available.
func Inspect(ctx context.Context, runner execx.Runner, name string, resolved config.Resolved) Report {
	r := Report{
		Name:        name,
		DisplayName: config.DisplayName(resolved.Tree, name),
		Category:    config.CategoryLabel(resolved.Tree, config.CategoryApp),
		Binary:      binaryPath(resolved.Tree),
	}

	var info os.FileInfo
	switch fi, err := os.Stat(r.Binary); {
	case r.Binary == "":
		r.Status = "no binary configured"
		return r
	case err != nil:
		r.Status = "not installed"
		return r
	case fi.IsDir():
		info = fi
		r.Permissions = modeString(info)
		r.Status = "error: " + r.Binary + " is a directory"
		return r
	case fi.Mode().Perm()&0o111 == 0:
		info = fi
		r.Permissions = modeString(info)
		r.Installed = true
		r.Status = "error: " + r.Binary + " is not executable"
		return r
	default:
		info = fi
		r.Permissions = modeString(info)
	}
	r.Installed = true

	// Resolve the binary's owner user/group from the stat info (Linux *syscall.Stat_t).
	if info != nil {
		if sys, ok := info.Sys().(*syscall.Stat_t); ok {
			uidStr := fmt.Sprintf("%d", sys.Uid)
			if u, err := user.LookupId(uidStr); err == nil && u.Username != "" {
				r.User = u.Username
			} else {
				r.User = uidStr
			}
			gidStr := fmt.Sprintf("%d", sys.Gid)
			if g, err := user.LookupGroupId(gidStr); err == nil && g.Name != "" {
				r.Group = g.Name
			} else {
				r.Group = gidStr
			}
		}
	}

	health := probeCommandFor(resolved.Tree, "health")
	version := probeCommandFor(resolved.Tree, "version")
	if len(health.argv) > 0 {
		r.OK, r.Status = runExitProbe(ctx, runner, health)
		if !r.OK && health.optional {
			r.OK = true
			r.Status = "ok"
		}
		if r.OK && len(version.argv) > 0 {
			r.Version, r.VersionShort = captureVersion(ctx, runner, resolved.Tree, version)
		}
		return r
	}

	if len(version.argv) == 0 {
		r.OK = true
		r.Status = "ok"
		return r
	}

	ok, status, raw, short := runVersionProbe(ctx, runner, resolved.Tree, version)
	if !ok && version.optional {
		r.OK = true
		r.Status = "ok"
		return r
	}
	r.OK = ok
	r.Status = status
	r.Version = raw
	r.VersionShort = short
	return r
}

func runExitProbe(ctx context.Context, runner execx.Runner, cmd probeCommand) (bool, string) {
	res, err := execx.Run(ctx, runner, probeTimeout, cmd.argv[0], cmd.argv[1:]...)
	switch {
	case err != nil && res.ExitCode == 0:
		return false, "error: " + err.Error()
	case res.ExitCode != cmd.expectExit:
		return false, fmt.Sprintf("error: exit %d (want %d)", res.ExitCode, cmd.expectExit)
	default:
		return true, "ok"
	}
}

func runVersionProbe(ctx context.Context, runner execx.Runner, tree map[string]any, cmd probeCommand) (bool, string, string, string) {
	res, err := execx.Run(ctx, runner, probeTimeout, cmd.argv[0], cmd.argv[1:]...)
	switch {
	case err != nil && res.ExitCode == 0:
		return false, "error: " + err.Error(), "", ""
	case res.ExitCode != cmd.expectExit:
		status := fmt.Sprintf("error: exit %d (want %d)", res.ExitCode, cmd.expectExit)
		if line := checks.FirstNonEmptyLine(res.Stderr); line != "" {
			status += ": " + line
		}
		return false, status, "", ""
	}
	if ok, detail := cmd.stdout.Match(res.Stdout); !ok {
		return false, "error: stdout " + detail, "", ""
	}
	if ok, detail := cmd.stderr.Match(res.Stderr); !ok {
		return false, "error: stderr " + detail, "", ""
	}
	raw := checks.FirstNonEmptyLine(res.Stdout)
	if raw == "" {
		raw = checks.FirstNonEmptyLine(res.Stderr)
	}
	return true, "ok", raw, shortVersionFor(ctx, runner, tree, raw)
}

func captureVersion(ctx context.Context, runner execx.Runner, tree map[string]any, cmd probeCommand) (string, string) {
	ok, _, raw, short := runVersionProbe(ctx, runner, tree, cmd)
	if !ok {
		return "", ""
	}
	return raw, short
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
	if vc := probeCommandFor(tree, "version_short"); len(vc.argv) > 0 {
		res, err := execx.Run(ctx, runner, probeTimeout, vc.argv[0], vc.argv[1:]...)
		if err == nil || res.ExitCode != 0 {
			if line := checks.FirstNonEmptyLine(res.Stdout); line != "" {
				return line
			}
			if line := checks.FirstNonEmptyLine(res.Stderr); line != "" {
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

// probeCommand is a daemon's resolved app probe command and the expectations
// its result must meet: the exit code and optional stdout/stderr matchers.
type probeCommand struct {
	argv       []string
	expectExit int
	optional   bool
	stdout     checks.OutputMatcher
	stderr     checks.OutputMatcher
}

// probeCommandFor returns a daemon's app probe command and outcome expectations
// for the named entry (`health`, `version` or `version_short`), resolved through
// the shared preflight-then-commands lookup. argv is nil when no such command is
// configured.
func probeCommandFor(tree map[string]any, key string) probeCommand {
	entry := checks.ReservedCommandEntry(tree, key)
	if entry == nil {
		return probeCommand{}
	}
	vc := probeCommand{argv: cfgval.StringList(entry["command"])}
	if v, ok := cfgval.Int(entry["expect_exit"]); ok {
		vc.expectExit = v
	}
	vc.optional = cfgval.Bool(entry["optional"])
	vc.stdout, _ = checks.ParseOutputMatcher(entry["expect_stdout"])
	vc.stderr, _ = checks.ParseOutputMatcher(entry["expect_stderr"])
	return vc
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
