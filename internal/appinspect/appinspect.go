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
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/output"
	"sermo/internal/process"
)

// probeTimeout bounds each app probe command invocation.
const probeTimeout = 5 * time.Second

const binaryExecutableModeMask = 0o111

const (
	// StatusOK reports a successful application inspection.
	StatusOK = "ok"
	// StatusNotInstalled reports that the configured binary is absent.
	StatusNotInstalled = "not installed"
	// StatusNoBinaryConfigured reports that the application has no binary path.
	StatusNoBinaryConfigured = "no binary configured"
	// StatusPrefixError prefixes application inspection failures.
	StatusPrefixError = "error:"
	// StatusPrefixNotInstalled prefixes version-identity misses.
	StatusPrefixNotInstalled = StatusNotInstalled + ":"
)

const (
	statusErrorPrefix               = StatusPrefixError + " "
	statusNotInstalledVersionPrefix = StatusPrefixNotInstalled + " version "
	statusCurrentLabel              = "current"
	statusVersionFromCycle          = "version_from cycle"
)

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
	// Output is the bounded stdout/stderr of the failing version/health probe,
	// set only on error so the app's monitoring event can explain WHY it failed.
	Output string `json:"output,omitempty"`
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

// List inspects every catalog entry in category. When includeMissing is false,
// only installed entries are returned. The order follows
// config.CatalogNamesInCategory, which sorts by name.
func List(ctx context.Context, runner execx.Runner, cfg *config.Config, category string, includeMissing bool, opts ...Option) []Report {
	if cfg == nil {
		return nil
	}
	var reports []Report
	cache := map[string]Report{}
	for _, name := range cfg.CatalogNamesInCategory(category) {
		r := inspectCatalog(ctx, runner, cfg, category, name, cache, map[string]bool{}, opts...)
		if !r.Installed && !includeMissing {
			continue
		}
		reports = append(reports, r)
	}
	applyCurrentLabels(reports, cfg, category)
	return reports
}

// InspectOne inspects a single catalog application by name (resolving
// version_from chains like List does for that app). It is used by the daemon's
// per-app monitoring, which inspects one app per cycle rather than the whole
// list.
func InspectOne(ctx context.Context, runner execx.Runner, cfg *config.Config, name string, opts ...Option) Report {
	return InspectCategoryOne(ctx, runner, cfg, config.CategoryApp, name, opts...)
}

// InspectCategoryOne inspects one catalog entry from category, resolving its
// version_from chain. Web inventories use it to share the same bounded parallel
// inspection path for applications and libraries.
func InspectCategoryOne(ctx context.Context, runner execx.Runner, cfg *config.Config, category, name string, opts ...Option) Report {
	if cfg == nil {
		return Report{Name: name}
	}
	return inspectCatalog(ctx, runner, cfg, category, name, map[string]Report{}, map[string]bool{}, opts...)
}

func applyCurrentLabels(reports []Report, cfg *config.Config, category string) {
	if category != config.CategoryApp || cfg == nil {
		return
	}
	byName := make(map[string]int, len(reports))
	for i := range reports {
		byName[reports[i].Name] = i
	}
	for i := range reports {
		doc := cfg.Apps[reports[i].Name]
		if doc == nil || !doc.TemplateCurrentLabel || doc.TemplateBaseName == "" || doc.Name == doc.TemplateBaseName {
			continue
		}
		if !reports[i].Installed || !reports[i].OK || reports[i].VersionShort == "" || hasCurrentLabel(reports[i].DisplayName) {
			continue
		}
		baseIdx, ok := byName[doc.TemplateBaseName]
		if !ok {
			continue
		}
		base := reports[baseIdx]
		if !base.Installed || !base.OK || base.VersionShort == "" || base.VersionShort != reports[i].VersionShort {
			continue
		}
		reports[i].DisplayName = strings.TrimSpace(reports[i].DisplayName + " " + statusCurrentLabel)
	}
}

func hasCurrentLabel(displayName string) bool {
	return slices.Contains(strings.Fields(displayName), statusCurrentLabel)
}

func inspectCatalog(ctx context.Context, runner execx.Runner, cfg *config.Config, category, name string, cache map[string]Report, chain map[string]bool, opts ...Option) Report {
	if category != config.CategoryApp {
		resolved, _ := cfg.ResolveCatalog(category, name)
		return inspectResolved(ctx, runner, name, resolved, category, opts...)
	}
	if doc, ok := cfg.Apps[name]; ok {
		name = doc.Name
	}
	if cached, ok := cache[name]; ok {
		return cached
	}
	if chain[name] {
		return Report{Name: name, Status: statusVersionFromCycle}
	}
	chain[name] = true
	resolved, _ := cfg.ResolveCatalog(category, name)
	r := inspectResolved(ctx, runner, name, resolved, category, opts...)
	if r.Installed && r.OK && r.Version == "" {
		fillVersionFrom(ctx, runner, cfg, &r, resolved.Tree, cache, chain, opts...)
	}
	delete(chain, name)
	cache[name] = r
	return r
}

func fillVersionFrom(ctx context.Context, runner execx.Runner, cfg *config.Config, r *Report, tree map[string]any, cache map[string]Report, chain map[string]bool, opts ...Option) {
	source := cfgval.String(tree[config.AppKeyVersionFrom])
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

// Inspect probes a single resolved catalog application: it stats the binary,
// runs health to confirm it runs when configured, and captures the version when
// available.
func Inspect(ctx context.Context, runner execx.Runner, name string, resolved config.Resolved, opts ...Option) Report {
	return inspectResolved(ctx, runner, name, resolved, config.CategoryApp, opts...)
}

func inspectResolved(
	ctx context.Context,
	runner execx.Runner,
	name string,
	resolved config.Resolved,
	category string,
	opts ...Option,
) Report {
	options := inspectOptions(opts)
	lookup := options.userLookup
	if lookup == nil {
		lookup = process.DefaultUserLookup()
	}
	r := Report{
		Name:        name,
		DisplayName: config.DisplayName(resolved.Tree, name),
		Category:    config.CategoryLabel(resolved.Tree, category),
		Binary:      catalogPath(resolved.Tree, category),
	}

	var info os.FileInfo
	switch fi, err := os.Stat(r.Binary); {
	case r.Binary == "":
		r.Status = StatusNoBinaryConfigured
		return r
	case err != nil:
		r.Status = StatusNotInstalled
		return r
	case fi.IsDir():
		info = fi
		r.Permissions = modeString(info)
		r.Status = statusErrorPrefix + r.Binary + " is a directory"
		return r
	case category == config.CategoryLibrary && fi.Size() == 0:
		info = fi
		r.Permissions = modeString(info)
		r.Installed = true
		r.Status = statusErrorPrefix + r.Binary + " is empty"
		return r
	case category != config.CategoryLibrary && fi.Mode().Perm()&binaryExecutableModeMask == 0:
		info = fi
		r.Permissions = modeString(info)
		r.Installed = true
		r.Status = statusErrorPrefix + r.Binary + " is not executable"
		return r
	default:
		info = fi
		r.Permissions = modeString(info)
	}
	r.Installed = true

	setReportOwner(&r, info, lookup)

	health := probeCommandFor(resolved.Tree, checks.DataKeyHealth)
	version := probeCommandFor(resolved.Tree, checks.DataKeyVersion)
	if len(health.argv) > 0 {
		var healthOut string
		r.OK, r.Status, healthOut = runExitProbe(ctx, runner, health)
		if !r.OK && health.optional {
			r.OK = true
			r.Status = StatusOK
		} else if !r.OK {
			r.Output = healthOut
		}
		if r.OK && len(version.argv) > 0 {
			vres := runVersionProbe(ctx, runner, resolved.Tree, version)
			if !vres.ok && (vres.identityMismatch || version.identityRequired()) {
				r.Installed = false
				r.OK = false
				r.Status = versionIdentityStatus(vres.status)
				r.Output = vres.output
				return r
			}
			if vres.ok {
				r.Version = vres.raw
				r.VersionShort = vres.short
			}
		}
		return r
	}

	if len(version.argv) == 0 {
		r.OK = true
		r.Status = StatusOK
		return r
	}

	vres := runVersionProbe(ctx, runner, resolved.Tree, version)
	if !vres.ok && (vres.identityMismatch || version.identityRequired()) {
		r.Installed = false
		r.OK = false
		r.Status = versionIdentityStatus(vres.status)
		r.Output = vres.output
		return r
	}
	if !vres.ok && (version.optional || options.versionOptional) {
		r.OK = true
		r.Status = StatusOK
		return r
	}
	r.OK = vres.ok
	r.Status = vres.status
	r.Output = vres.output
	r.Version = vres.raw
	r.VersionShort = vres.short
	return r
}

func versionIdentityStatus(status string) string {
	if strings.HasPrefix(status, StatusPrefixNotInstalled) {
		return status
	}
	return statusNotInstalledVersionPrefix + strings.TrimPrefix(status, statusErrorPrefix)
}

func setReportOwner(r *Report, info os.FileInfo, lookup *process.UserLookup) {
	if info == nil || lookup == nil {
		return
	}
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	r.User = lookupIDName(sys.Uid, lookup.Username)
	r.Group = lookupIDName(sys.Gid, lookup.GroupName)
}

func lookupIDName(id uint32, lookup func(uint32) string) string {
	if name := lookup(id); name != "" {
		return name
	}
	return fmt.Sprintf("%d", id)
}

func inspectOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

func runExitProbe(ctx context.Context, runner execx.Runner, cmd probeCommand) (bool, string, string) {
	res, err := runProbeCommand(ctx, runner, cmd)
	switch {
	case res.ExitCode == execx.ExitCodeRunFailure:
		msg := execx.OperatorFailure(err, res, cmd.timeout)
		if msg == "" {
			msg = execx.CommandDidNotStart
		}
		return false, statusErrorPrefix + msg, output.Bounded(res.Stdout, res.Stderr)
	case err != nil && res.ExitCode == checks.CommandDefaultExpectedExit:
		return false, statusErrorPrefix + err.Error(), output.Bounded(res.Stdout, res.Stderr)
	case !checks.ExitCodeExpected(res.ExitCode, cmd.expectExit):
		return false, fmt.Sprintf("%sexit %d (want %s)", statusErrorPrefix, res.ExitCode, checks.ExpectExitText(cmd.expectExit)), output.Bounded(res.Stdout, res.Stderr)
	default:
		return true, StatusOK, ""
	}
}

type versionProbeResult struct {
	ok               bool
	identityMismatch bool
	status           string
	output           string // bounded stdout/stderr, set on failure
	raw              string
	short            string
}

func runVersionProbe(ctx context.Context, runner execx.Runner, tree map[string]any, cmd probeCommand) versionProbeResult {
	res, err := runProbeCommand(ctx, runner, cmd)
	fail := func(status string) versionProbeResult {
		return versionProbeResult{status: status, output: output.Bounded(res.Stdout, res.Stderr)}
	}
	switch {
	case res.ExitCode == execx.ExitCodeRunFailure:
		msg := execx.OperatorFailure(err, res, cmd.timeout)
		if msg == "" {
			msg = execx.CommandDidNotStart
		}
		return fail(statusErrorPrefix + msg)
	case err != nil && res.ExitCode == checks.CommandDefaultExpectedExit:
		return fail(statusErrorPrefix + err.Error())
	case !checks.ExitCodeExpected(res.ExitCode, cmd.expectExit):
		status := fmt.Sprintf("%sexit %d (want %s)", statusErrorPrefix, res.ExitCode, checks.ExpectExitText(cmd.expectExit))
		if line := output.FirstNonEmptyLine(res.Stderr); line != "" {
			status += ": " + line
		}
		return fail(status)
	}
	if ok, detail := cmd.stdout.Match(res.Stdout); !ok {
		return fail(statusErrorPrefix + "stdout " + detail)
	}
	if ok, detail := cmd.stderr.Match(res.Stderr); !ok {
		return fail(statusErrorPrefix + "stderr " + detail)
	}
	if cmd.versionMatchWarn != "" {
		return fail(statusErrorPrefix + "version_match " + cmd.versionMatchWarn)
	}
	if ok, detail := cmd.versionMatch.Match(checks.VersionOutput(res.Stdout, res.Stderr)); !ok {
		return versionProbeResult{identityMismatch: true, status: statusNotInstalledVersionPrefix + detail, output: output.Bounded(res.Stdout, res.Stderr)}
	}
	raw := output.FirstNonEmptyLine(res.Stdout)
	if raw == "" {
		raw = output.FirstNonEmptyLine(res.Stderr)
	}
	return versionProbeResult{ok: true, status: StatusOK, raw: raw, short: shortVersionFor(ctx, runner, tree, raw)}
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
	if vc := probeCommandFor(tree, checks.DataKeyVersionShort); len(vc.argv) > 0 {
		res, err := runProbeCommand(ctx, runner, vc)
		if err == nil && res.ExitCode == checks.CommandDefaultExpectedExit {
			if line := output.FirstNonEmptyLine(res.Stdout); line != "" {
				return line
			}
			if line := output.FirstNonEmptyLine(res.Stderr); line != "" {
				return line
			}
		}
	}
	return checks.ShortVersion(rawVersion)
}

func runProbeCommand(ctx context.Context, runner execx.Runner, cmd probeCommand) (execx.Result, error) {
	timeout := cmd.timeout
	if timeout <= 0 {
		timeout = probeTimeout
	}
	if cmd.user != "" {
		return execx.RunUser(ctx, runner, timeout, cmd.user, cmd.argv[0], cmd.argv[1:]...)
	}
	return execx.Run(ctx, runner, timeout, cmd.argv[0], cmd.argv[1:]...)
}

// catalogPath returns the resolved binary or library file path for a catalog
// entry. Libraries prefer their preflight file check because shared objects are
// expected to be readable files rather than executable binaries.
func catalogPath(tree map[string]any, category string) string {
	if category == config.CategoryLibrary {
		if pf, ok := tree[config.SectionPreflight].(map[string]any); ok {
			if file, ok := pf[checks.CheckTypeFile].(map[string]any); ok {
				if p := cfgval.AsString(file[checks.CheckKeyPath]); p != "" {
					return p
				}
			}
		}
	}
	return binaryPath(tree)
}

// binaryPath returns the resolved binary path of a daemon: its preflight
// `binary` check path when present, otherwise the `binary` variable.
func binaryPath(tree map[string]any) string {
	if pf, ok := tree[config.SectionPreflight].(map[string]any); ok {
		if bin, ok := pf[checks.CheckTypeBinary].(map[string]any); ok {
			if p := cfgval.AsString(bin[checks.CheckKeyPath]); p != "" {
				return p
			}
		}
		if p := firstNamespacedBinaryPath(pf); p != "" {
			return p
		}
	}
	if vars, ok := tree[config.SectionVariables].(map[string]any); ok {
		if p := cfgval.AsString(vars[config.VariableKeyBinary]); p != "" {
			return p
		}
	}
	return config.DocumentBinary(tree)
}

func firstNamespacedBinaryPath(preflight map[string]any) string {
	for _, prefix := range namespacedBinaryPrefixes(preflight) {
		if bin, ok := preflight[prefix+"-"+checks.CheckTypeBinary].(map[string]any); ok {
			if p := cfgval.AsString(bin[checks.CheckKeyPath]); p != "" {
				return p
			}
		}
	}
	return ""
}

func namespacedBinaryPrefixes(preflight map[string]any) []string {
	prefixes := make([]string, 0, len(preflight))
	for key, raw := range preflight {
		prefix, ok := strings.CutSuffix(key, "-"+checks.CheckTypeBinary)
		if !ok || prefix == "" {
			continue
		}
		entry, ok := raw.(map[string]any)
		if !ok || cfgval.AsString(entry[checks.CheckKeyType]) != checks.CheckTypeBinary || cfgval.AsString(entry[checks.CheckKeyPath]) == "" {
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
	argv             []string
	user             string
	timeout          time.Duration
	expectExit       []int
	optional         bool
	stdout           checks.OutputMatcher
	stderr           checks.OutputMatcher
	versionMatch     checks.VersionMatcher
	versionMatchWarn string
}

func (cmd probeCommand) identityRequired() bool {
	return cmd.versionMatch.Active() || cmd.versionMatchWarn != ""
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
	vc := probeCommand{
		argv:       cfgval.StringList(entry[checks.CheckKeyCommand]),
		user:       cfgval.String(entry[checks.CheckKeyUser]),
		timeout:    cfgval.DurationOr(entry[checks.CheckKeyTimeout], probeTimeout),
		expectExit: []int{checks.CommandDefaultExpectedExit},
	}
	if v, ok := cfgval.IntList(entry[checks.CheckKeyExpectExit]); ok {
		vc.expectExit = v
	}
	vc.optional = cfgval.Bool(entry[checks.CheckKeyOptional])
	vc.stdout, _ = checks.ParseOutputMatcher(entry[checks.CheckKeyExpectStdout])
	vc.stderr, _ = checks.ParseOutputMatcher(entry[checks.CheckKeyExpectStderr])
	if key == checks.DataKeyVersion {
		vc.versionMatch, vc.versionMatchWarn = checks.ParseVersionMatcher(entry[checks.CheckKeyVersionMatch])
		if !vc.versionMatch.Active() && vc.versionMatchWarn == "" {
			vc.versionMatch, vc.versionMatchWarn = checks.ParseVersionMatcher(tree[checks.CheckKeyVersionMatch])
		}
	}
	return vc
}

func namespacedReservedCommandEntry(tree map[string]any, key string) map[string]any {
	preflight, ok := tree[config.SectionPreflight].(map[string]any)
	if !ok {
		return nil
	}
	for _, prefix := range namespacedBinaryPrefixes(preflight) {
		entry, ok := preflight[prefix+"-"+key].(map[string]any)
		if ok && len(cfgval.StringList(entry[checks.CheckKeyCommand])) > 0 {
			return entry
		}
	}
	return nil
}

// ShortVersion parsing now lives in internal/checks (checks.ShortVersion) so the
// version-change monitor can reuse it without an import cycle.
