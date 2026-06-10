// Package cli implements the sermoctl command-line interface.
package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/execx"
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

// runApps lists the applications (daemons under daemons/apps): which are
// installed (their binary is present and executable), the version their version
// command reports, and whether they resolve without error. Only installed apps
// are shown unless `apps all` is given.
func (a App) runApps(ctx context.Context, opts options) int {
	return a.listCategory(ctx, opts, config.CategoryApp, "apps", "installed applications")
}

// runLibs lists the library daemons (daemons/libs) services can watch for
// changes, with the version each reports and whether it is present.
func (a App) runLibs(ctx context.Context, opts options) int {
	return a.listCategory(ctx, opts, config.CategoryLibrary, "libs", "libraries")
}

// runServices lists the service daemons (daemons/services and the root): which
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
	for _, name := range cfg.DaemonsInCategory(category) {
		resolved, _ := cfg.ResolveDaemon(name)
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

// inspectApp probes a single resolved daemon: it stats the binary and, when
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

	vc := appVersionCommand(resolved.Tree)
	if len(vc.argv) == 0 {
		r.OK = true
		r.Status = "ok"
		return r
	}

	res, err := execx.Run(ctx, a.Runner, 5*time.Second, vc.argv[0], vc.argv[1:]...)
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

// appBinary returns the resolved binary path of a daemon: its preflight `binary`
// check path when present, otherwise the `binary` variable.
func appBinary(tree map[string]any) string {
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

// appVersionCommand returns a daemon's version command and outcome expectations,
// looked up in `preflight.version` then `commands.version`. argv is nil when no
// version command is configured.
func appVersionCommand(tree map[string]any) versionCommand {
	for _, src := range []string{"preflight", "commands"} {
		section, ok := tree[src].(map[string]any)
		if !ok {
			continue
		}
		entry, ok := section["version"].(map[string]any)
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

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
