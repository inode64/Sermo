package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"sermo/internal/config"
)

// runDaemon dispatches `daemon list` and `daemon show DAEMON` (post-MVP).
func (a App) runDaemon(opts options) int {
	if len(opts.args) == 0 {
		fmt.Fprintln(a.Stderr, "usage error: daemon requires a subcommand (list|show)")
		return exitUsage
	}
	cfg, code := a.loadConfig(opts)
	if code != exitSuccess {
		return code
	}

	switch opts.args[0] {
	case "list":
		a.printNamed(opts, sortedUnique(cfg.Daemons), cfg.Daemons, "daemons")
		return exitSuccess
	case "show":
		if len(opts.args) < 2 {
			fmt.Fprintln(a.Stderr, "usage error: daemon show requires a daemon name")
			return exitUsage
		}
		name := opts.args[1]
		doc, ok := cfg.Daemons[name]
		if !ok {
			a.reportError(opts, fmt.Sprintf("unknown daemon %q", name))
			return exitRuntimeError
		}
		return a.renderTree(opts, config.Resolved{Name: name, Tree: doc.Body})
	default:
		fmt.Fprintf(a.Stderr, "usage error: unknown daemon subcommand %q\n", opts.args[0])
		return exitUsage
	}
}

// runService dispatches `service list`, `service show SERVICE` and
// `service clone SOURCE TARGET` (post-MVP).
func (a App) runService(opts options) int {
	if len(opts.args) == 0 {
		fmt.Fprintln(a.Stderr, "usage error: service requires a subcommand (list|show|clone)")
		return exitUsage
	}
	cfg, code := a.loadConfig(opts)
	if code != exitSuccess {
		return code
	}

	switch opts.args[0] {
	case "list":
		a.printNamed(opts, sortedUnique(cfg.Services), cfg.Services, "services")
		return exitSuccess
	case "show":
		if len(opts.args) < 2 {
			fmt.Fprintln(a.Stderr, "usage error: service show requires a service name")
			return exitUsage
		}
		return a.showResolvedService(opts, cfg, opts.args[1])
	case "clone":
		if len(opts.args) < 3 {
			fmt.Fprintln(a.Stderr, "usage error: service clone requires SOURCE and TARGET")
			return exitUsage
		}
		return a.cloneService(opts, cfg, opts.args[1], opts.args[2])
	default:
		fmt.Fprintf(a.Stderr, "usage error: unknown service subcommand %q\n", opts.args[0])
		return exitUsage
	}
}

func (a App) showResolvedService(opts options, cfg *config.Config, name string) int {
	if code := a.requireService(opts, cfg, name); code != exitSuccess {
		return code
	}
	resolved, code := a.resolveService(opts, cfg, name)
	if code != exitSuccess {
		return code
	}
	return a.renderTree(opts, resolved)
}

// cloneService writes a new included service that clones SOURCE.
func (a App) cloneService(opts options, cfg *config.Config, source, target string) int {
	if _, ok := cfg.Services[source]; !ok {
		a.reportError(opts, fmt.Sprintf("unknown source service %q", source))
		return exitRuntimeError
	}
	if _, ok := cfg.Services[target]; ok {
		a.reportError(opts, fmt.Sprintf("target service %q already exists", target))
		return exitRuntimeError
	}
	if len(cfg.Global.Includes) == 0 {
		a.reportError(opts, "no include directory configured (paths.includes)")
		return exitRuntimeError
	}

	dir := cfg.Global.Includes[0]
	path := filepath.Join(dir, target+".yml")
	content := fmt.Sprintf("kind: service\nname: %s\nclone: %s\n", target, source)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // G306: generated service YAML is non-sensitive (0644)
		a.reportError(opts, fmt.Sprintf("write %s: %v", path, err))
		return exitRuntimeError
	}
	fmt.Fprintf(a.Stdout, "created %s\n", path)
	return exitSuccess
}

// runConfigDiff renders two resolved services and reports their line difference.
func (a App) runConfigDiff(globalPath string, rest []string, opts options) int {
	if len(rest) < 2 {
		fmt.Fprintln(a.Stderr, "usage error: config diff requires BASE and SERVICE")
		return exitUsage
	}
	cfg, err := a.LoadConfig(globalPath)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("load config failed: %v", err))
		return exitRuntimeError
	}

	base, code := a.renderForDiff(opts, cfg, rest[0])
	if code != exitSuccess {
		return code
	}
	other, code := a.renderForDiff(opts, cfg, rest[1])
	if code != exitSuccess {
		return code
	}

	removed, added := lineDiff(base, other)
	identical := len(removed) == 0 && len(added) == 0
	if opts.json {
		writeJSON(a.Stdout, map[string]any{
			"base":      rest[0],
			"service":   rest[1],
			"identical": identical,
			"removed":   removed,
			"added":     added,
		})
		return exitSuccess
	}
	if identical {
		fmt.Fprintf(a.Stdout, "%s and %s resolve identically\n", rest[0], rest[1])
		return exitSuccess
	}
	fmt.Fprintf(a.Stdout, "--- %s\n+++ %s\n", rest[0], rest[1])
	for _, l := range removed {
		fmt.Fprintf(a.Stdout, "- %s\n", l)
	}
	for _, l := range added {
		fmt.Fprintf(a.Stdout, "+ %s\n", l)
	}
	return exitSuccess
}

func (a App) renderForDiff(opts options, cfg *config.Config, name string) (string, int) {
	if code := a.requireService(opts, cfg, name); code != exitSuccess {
		return "", code
	}
	resolved, code := a.resolveService(opts, cfg, name)
	if code != exitSuccess {
		return "", code
	}
	data, err := config.RenderYAML(resolved)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("render %s: %v", name, err))
		return "", exitRuntimeError
	}
	return string(data), exitSuccess
}

// requireService reports an unknown-service error unless name is configured.
// It returns exitSuccess when the service exists.
func (a App) requireService(opts options, cfg *config.Config, name string) int {
	if _, ok := cfg.Services[name]; !ok {
		a.reportError(opts, fmt.Sprintf("unknown service %q", name))
		return exitRuntimeError
	}
	return exitSuccess
}

// resolveService resolves name into its flat tree, printing the scoped
// resolution issues on failure. It returns exitSuccess when resolution is clean.
func (a App) resolveService(opts options, cfg *config.Config, name string) (config.Resolved, int) {
	resolved, errs := cfg.Resolve(name)
	if len(errs) > 0 {
		a.printIssues(opts, scopedIssues(name, errs))
		return config.Resolved{}, exitConfigInvalid
	}
	return resolved, exitSuccess
}

func (a App) loadConfig(opts options) (*config.Config, int) {
	globalPath := opts.globalPath()
	cfg, err := a.LoadConfig(globalPath)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("load config failed: %v", err))
		return nil, exitRuntimeError
	}
	return cfg, exitSuccess
}

func (a App) renderTree(opts options, r config.Resolved) int {
	var data []byte
	var err error
	if opts.json {
		data, err = config.RenderJSON(r)
	} else {
		data, err = config.RenderYAML(r)
	}
	if err != nil {
		a.reportError(opts, fmt.Sprintf("render %s: %v", r.Name, err))
		return exitRuntimeError
	}
	_, _ = a.Stdout.Write(data)
	if n := len(data); n == 0 || data[n-1] != '\n' {
		fmt.Fprintln(a.Stdout)
	}
	return exitSuccess
}

// printNamed lists documents alongside their display_name. In text mode it
// prints "name<TAB>Display Name" (omitting the suffix when the display name is
// just the id); in JSON it emits objects with name and display_name.
func (a App) printNamed(opts options, names []string, docs map[string]*config.Document, kind string) {
	if opts.json {
		out := make([]map[string]string, 0, len(names))
		for _, n := range names {
			display := n
			if doc, ok := docs[n]; ok {
				display = config.DisplayName(doc.Body, n)
			}
			out = append(out, map[string]string{"name": n, "display_name": display})
		}
		writeJSON(a.Stdout, map[string]any{kind: out})
		return
	}
	if len(names) == 0 {
		fmt.Fprintf(a.Stdout, "no %s\n", kind)
		return
	}
	for _, n := range names {
		display := n
		if doc, ok := docs[n]; ok {
			display = config.DisplayName(doc.Body, n)
		}
		if display == n {
			fmt.Fprintln(a.Stdout, n)
		} else {
			fmt.Fprintf(a.Stdout, "%s\t%s\n", n, display)
		}
	}
}

func sortedUnique[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// lineDiff returns the lines present only in base and only in other. Both
// renders are deterministic key-sorted YAML, so this is a readable approximation
// of a structural diff.
func lineDiff(base, other string) (removed, added []string) {
	baseSet := lineCount(base)
	otherSet := lineCount(other)
	for _, l := range strings.Split(strings.TrimRight(base, "\n"), "\n") {
		if otherSet[l] == 0 && !slices.Contains(removed, l) {
			removed = append(removed, l)
		}
	}
	for _, l := range strings.Split(strings.TrimRight(other, "\n"), "\n") {
		if baseSet[l] == 0 && !slices.Contains(added, l) {
			added = append(added, l)
		}
	}
	return removed, added
}

func lineCount(s string) map[string]int {
	out := map[string]int{}
	for _, l := range strings.Split(s, "\n") {
		out[l]++
	}
	return out
}
