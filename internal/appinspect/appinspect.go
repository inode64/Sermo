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
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/process"
)

// probeTimeout bounds each app probe command invocation.
const probeTimeout = 5 * time.Second

// Report is one application's installed/version/health summary.
type Report struct {
	Name          string `json:"name"`
	DisplayName   string `json:"display_name"`
	Category      string `json:"category,omitempty"`
	Binary        string `json:"binary"`
	Permissions   string `json:"permissions,omitempty"`
	User          string `json:"user,omitempty"`
	Group         string `json:"group,omitempty"`
	Version       string `json:"version"`
	VersionShort  string `json:"version_short"`
	VersionSource string `json:"version_source,omitempty"`
	Installed     bool   `json:"installed"`
	OK            bool   `json:"ok"`
	Status        string `json:"status"`
}

type options struct {
	userLookup      *process.UserLookup
	versionOptional bool
}

// Option customizes application inspection.
type Option func(*options)

// WithUserLookup sets the user/group resolver used for owner display.
func WithUserLookup(lookup *process.UserLookup) Option {
	return func(o *options) { o.userLookup = lookup }
}

// WithOptionalVersion treats a failed version command as an unknown version,
// leaving installation status based on the binary/health checks. This is useful
// for service catalog inventory, where the service being installable matters
// more than a distro-specific version flag.
func WithOptionalVersion() Option {
	return func(o *options) { o.versionOptional = true }
}

// List inspects every catalog daemon in the category. When includeMissing is
// false only installed applications (binary present) are returned. The order
// follows config.DaemonsInCategory, which sorts by name.
func List(ctx context.Context, runner execx.Runner, cfg *config.Config, category string, includeMissing bool, opts ...Option) []Report {
	if cfg == nil {
		return nil
	}
	var reports []Report
	cache := map[string]Report{}
	for _, name := range cfg.DaemonsInCategory(category) {
		r := inspectCatalog(ctx, runner, cfg, category, name, cache, map[string]bool{}, opts...)
		if !r.Installed && !includeMissing {
			continue
		}
		reports = append(reports, r)
	}
	return reports
}

func inspectCatalog(ctx context.Context, runner execx.Runner, cfg *config.Config, category, name string, cache map[string]Report, chain map[string]bool, opts ...Option) Report {
	if category != config.CategoryApp {
		resolved, _ := cfg.ResolveCatalog(category, name)
		return Inspect(ctx, runner, name, resolved, opts...)
	}
	if doc, ok := cfg.Apps[name]; ok {
		name = doc.Name
	}
	if cached, ok := cache[name]; ok {
		return cached
	}
	if chain[name] {
		return Report{Name: name, Status: "version_from cycle"}
	}
	chain[name] = true
	resolved, _ := cfg.ResolveCatalog(category, name)
	r := Inspect(ctx, runner, name, resolved, opts...)
	if r.Installed && r.OK && r.Version == "" {
		fillVersionFrom(ctx, runner, cfg, &r, resolved.Tree, cache, chain, opts...)
	}
	delete(chain, name)
	cache[name] = r
	return r
}

func fillVersionFrom(ctx context.Context, runner execx.Runner, cfg *config.Config, r *Report, tree map[string]any, cache map[string]Report, chain map[string]bool, opts ...Option) {
	source := cfgval.String(tree["version_from"])
	if source == "" {
		return
	}
	provider, ok := cfg.Apps[source]
	if !ok || provider.Name == r.Name {
		return
	}
	report := inspectCatalog(ctx, runner, cfg, config.CategoryApp, provider.Name, cache, chain, opts...)
	if report.Version == "" {
		return
	}
	r.Version = report.Version
	r.VersionShort = report.VersionShort
	r.VersionSource = provider.Name
}

// Inspect probes a single resolved daemon: it stats the binary, runs health to
// confirm it runs when configured, and captures the version when available.
func Inspect(ctx context.Context, runner execx.Runner, name string, resolved config.Resolved, opts ...Option) Report {
	options := inspectOptions(opts)
	lookup := options.userLookup
	if lookup == nil {
		lookup = process.DefaultUserLookup()
	}
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
			if name := lookup.Username(sys.Uid); name != "" {
				r.User = name
			}
			if r.User == "" {
				r.User = fmt.Sprintf("%d", sys.Uid)
			}
			if name := lookup.GroupName(sys.Gid); name != "" {
				r.Group = name
			}
			if r.Group == "" {
				r.Group = fmt.Sprintf("%d", sys.Gid)
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
	if !ok && (version.optional || options.versionOptional) {
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

func inspectOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

func runExitProbe(ctx context.Context, runner execx.Runner, cmd probeCommand) (bool, string) {
	res, err := execx.Run(ctx, runner, probeTimeout, cmd.argv[0], cmd.argv[1:]...)
	switch {
	case err != nil && res.ExitCode == 0:
		return false, "error: " + err.Error()
	case !checks.ExitCodeExpected(res.ExitCode, cmd.expectExit):
		return false, fmt.Sprintf("error: exit %d (want %s)", res.ExitCode, checks.ExpectExitText(cmd.expectExit))
	default:
		return true, "ok"
	}
}

func runVersionProbe(ctx context.Context, runner execx.Runner, tree map[string]any, cmd probeCommand) (bool, string, string, string) {
	res, err := execx.Run(ctx, runner, probeTimeout, cmd.argv[0], cmd.argv[1:]...)
	switch {
	case err != nil && res.ExitCode == 0:
		return false, "error: " + err.Error(), "", ""
	case !checks.ExitCodeExpected(res.ExitCode, cmd.expectExit):
		status := fmt.Sprintf("error: exit %d (want %s)", res.ExitCode, checks.ExpectExitText(cmd.expectExit))
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
		if err == nil && res.ExitCode == 0 {
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
		if p := firstNamespacedBinaryPath(pf); p != "" {
			return p
		}
	}
	if vars, ok := tree["variables"].(map[string]any); ok {
		if p := cfgval.AsString(vars["binary"]); p != "" {
			return p
		}
	}
	return config.DocumentBinary(tree)
}

func firstNamespacedBinaryPath(preflight map[string]any) string {
	for _, prefix := range namespacedBinaryPrefixes(preflight) {
		if bin, ok := preflight[prefix+"-binary"].(map[string]any); ok {
			if p := cfgval.AsString(bin["path"]); p != "" {
				return p
			}
		}
	}
	return ""
}

func namespacedBinaryPrefixes(preflight map[string]any) []string {
	prefixes := make([]string, 0, len(preflight))
	for key, raw := range preflight {
		prefix, ok := strings.CutSuffix(key, "-binary")
		if !ok || prefix == "" {
			continue
		}
		entry, ok := raw.(map[string]any)
		if !ok || cfgval.AsString(entry["type"]) != "binary" || cfgval.AsString(entry["path"]) == "" {
			continue
		}
		prefixes = append(prefixes, prefix)
	}
	sort.Strings(prefixes)
	return prefixes
}

// probeCommand is a daemon's resolved app probe command and the expectations
// its result must meet: the exit code and optional stdout/stderr matchers.
type probeCommand struct {
	argv       []string
	expectExit []int
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
		entry = namespacedReservedCommandEntry(tree, key)
	}
	if entry == nil {
		return probeCommand{}
	}
	vc := probeCommand{argv: cfgval.StringList(entry["command"]), expectExit: []int{0}}
	if v, ok := cfgval.IntList(entry["expect_exit"]); ok {
		vc.expectExit = v
	}
	vc.optional = cfgval.Bool(entry["optional"])
	vc.stdout, _ = checks.ParseOutputMatcher(entry["expect_stdout"])
	vc.stderr, _ = checks.ParseOutputMatcher(entry["expect_stderr"])
	return vc
}

func namespacedReservedCommandEntry(tree map[string]any, key string) map[string]any {
	preflight, ok := tree["preflight"].(map[string]any)
	if !ok {
		return nil
	}
	for _, prefix := range namespacedBinaryPrefixes(preflight) {
		entry, ok := preflight[prefix+"-"+key].(map[string]any)
		if ok && len(cfgval.StringList(entry["command"])) > 0 {
			return entry
		}
	}
	return nil
}

// shortVersionRE matches the first dotted numeric version in a raw version line:
// a `major.minor` with an optional `.patch`. By stopping at three components it
// excludes any further build numbers (e.g. the `.1` in `2.8.4.1`) and trailing
// suffixes (`p18`, `-P1`, `(2)`, `-MariaDB`), which keeps only the version and
// at most the patchlevel. Requiring at least `major.minor` avoids latching onto
// stray single digits (e.g. the `5` in perl's "perl 5, version 42 ... (v5.42.0)").
var shortVersionRE = regexp.MustCompile(`[0-9]+\.[0-9]+(?:\.[0-9]+)?`)

// shortIntegerVersionRE covers projects that publish integer-only releases in
// version output, such as "pkexec version 126". It only runs after the dotted
// matcher misses so a line like "systemd 260 (260.1)" still reports "260.1".
var shortIntegerVersionRE = regexp.MustCompile(`(?i)\b(?:version|v)\s*:?\s*([0-9]+)\b`)

// ShortVersion reduces a raw version line (as captured in Report.Version) to
// just its numeric version, keeping at most three components
// (major.minor.patch). It returns the first dotted numeric token found, then a
// guarded integer-only version token, or "" when the line carries no
// recognizable version.
func ShortVersion(s string) string {
	if dotted := shortVersionRE.FindString(s); dotted != "" {
		return dotted
	}
	if match := shortIntegerVersionRE.FindStringSubmatch(s); len(match) > 1 {
		return match[1]
	}
	return ""
}
