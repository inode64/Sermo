// Package cli implements the sermoctl command-line interface.
package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"sermo/internal/config"
)

// appReport is one application's installed/version/health summary for `apps`.
type appReport struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Binary      string `json:"binary"`
	Version     string `json:"version"`
	Installed   bool   `json:"installed"`
	OK          bool   `json:"ok"`
	Status      string `json:"status"`
}

// runApps lists the applications (profiles under profiles/apps): which are
// installed (their binary is present and executable), the version their version
// command reports, and whether they resolve without error. Only installed apps
// are shown unless `apps all` is given.
func (a App) runApps(ctx context.Context, opts options) int {
	return a.listCategory(ctx, opts, config.CategoryApp, "apps", "installed applications")
}

// runLibs lists the library profiles (profiles/libs) services can watch for
// changes, with the version each reports and whether it is present.
func (a App) runLibs(ctx context.Context, opts options) int {
	return a.listCategory(ctx, opts, config.CategoryLibrary, "libs", "libraries")
}

// runServices lists the service profiles (profiles/services and the root): which
// are installed, the version their version command reports, and whether they
// resolve without error.
func (a App) runServices(ctx context.Context, opts options) int {
	return a.listCategory(ctx, opts, config.CategoryService, "services", "installed services")
}

func (a App) listCategory(ctx context.Context, opts options, category, jsonKey, empty string) int {
	includeMissing := len(opts.args) > 0 && opts.args[0] == "all"

	cfg, code := a.loadConfig(opts)
	if code != exitSuccess {
		return code
	}

	var reports []appReport
	for _, name := range cfg.ProfilesInCategory(category) {
		resolved, _ := cfg.ResolveProfile(name)
		r := a.inspectApp(ctx, name, resolved)
		if !r.Installed && !includeMissing {
			continue
		}
		reports = append(reports, r)
	}

	if opts.json {
		writeJSON(a.Stdout, map[string]any{jsonKey: reports})
		return exitSuccess
	}
	a.printApps(reports, empty)
	return exitSuccess
}

// inspectApp probes a single resolved profile: it stats the binary and, when
// present, runs the version command to capture the version and confirm it runs.
func (a App) inspectApp(ctx context.Context, name string, resolved config.Resolved) appReport {
	r := appReport{
		Name:        name,
		DisplayName: config.DisplayName(resolved.Tree, name),
		Binary:      appBinary(resolved.Tree),
	}

	switch info, err := os.Stat(r.Binary); {
	case r.Binary == "":
		r.Status = "no binary configured"
		return r
	case err != nil:
		r.Status = "not installed"
		return r
	case info.IsDir():
		r.Status = "error: " + r.Binary + " is a directory"
		return r
	case info.Mode().Perm()&0o111 == 0:
		r.Installed = true
		r.Status = "error: " + r.Binary + " is not executable"
		return r
	}
	r.Installed = true

	argv, expectExit := appVersionCommand(resolved.Tree)
	if len(argv) == 0 {
		r.OK = true
		r.Status = "ok"
		return r
	}

	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, err := a.Runner.Run(cctx, argv[0], argv[1:]...)
	switch {
	case err != nil && res.ExitCode == 0:
		r.Status = "error: " + err.Error()
	case res.ExitCode != expectExit:
		r.Status = fmt.Sprintf("error: exit %d (want %d)", res.ExitCode, expectExit)
		if line := firstNonEmptyLine(res.Stderr); line != "" {
			r.Status += ": " + line
		}
	default:
		r.OK = true
		r.Status = "ok"
		r.Version = firstNonEmptyLine(res.Stdout)
		if r.Version == "" {
			r.Version = firstNonEmptyLine(res.Stderr)
		}
	}
	return r
}

func (a App) printApps(reports []appReport, empty string) {
	if len(reports) == 0 {
		fmt.Fprintf(a.Stdout, "no %s\n", empty)
		return
	}
	tw := tabwriter.NewWriter(a.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "APPLICATION\tVERSION\tSTATUS")
	for _, r := range reports {
		version := r.Version
		if version == "" {
			version = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", r.DisplayName, version, r.Status)
	}
	_ = tw.Flush()
}

// appBinary returns the resolved binary path of a profile: its preflight `binary`
// check path when present, otherwise the `binary` variable.
func appBinary(tree map[string]any) string {
	if pf, ok := tree["preflight"].(map[string]any); ok {
		if bin, ok := pf["binary"].(map[string]any); ok {
			if p := asString(bin["path"]); p != "" {
				return p
			}
		}
	}
	if vars, ok := tree["variables"].(map[string]any); ok {
		return asString(vars["binary"])
	}
	return ""
}

// appVersionCommand returns the argv and expected exit code of a profile's version
// command, looked up in `preflight.version` then `commands.version`.
func appVersionCommand(tree map[string]any) ([]string, int) {
	for _, src := range []string{"preflight", "commands"} {
		section, ok := tree[src].(map[string]any)
		if !ok {
			continue
		}
		entry, ok := section["version"].(map[string]any)
		if !ok {
			continue
		}
		argv := asStringSlice(entry["command"])
		if len(argv) == 0 {
			continue
		}
		expect := 0
		if v, ok := entry["expect_exit"]; ok {
			expect = asInt(v)
		}
		return argv, expect
	}
	return nil, 0
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asStringSlice(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func asInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case uint64:
		return int(t)
	case float64:
		return int(t)
	case string:
		n, _ := strconv.Atoi(t)
		return n
	}
	return 0
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
