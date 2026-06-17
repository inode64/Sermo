package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"sermo/internal/config"
)

// runDaemon dispatches daemon catalog inspection and sermod control subcommands.
func (a App) runDaemon(ctx context.Context, opts options) int {
	if len(opts.args) == 0 {
		return a.usageError("daemon requires a subcommand (list|show|reload)")
	}
	if opts.args[0] == "reload" {
		if len(opts.args) > 1 {
			return a.usageError("daemon reload takes no arguments")
		}
		return a.runReload(ctx, opts)
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
			return a.usageError("daemon show requires a daemon name")
		}
		name := opts.args[1]
		doc, ok := cfg.Daemons[name]
		if !ok {
			return a.fail(opts, fmt.Sprintf("unknown daemon %q", name))
		}
		return a.renderTree(opts, config.Resolved{Name: name, Tree: doc.Body})
	default:
		return a.usageError(fmt.Sprintf("unknown daemon subcommand %q", opts.args[0]))
	}
}

// runService dispatches `service list`, `service show SERVICE` and
// `service clone SOURCE TARGET`.
func (a App) runService(opts options) int {
	if len(opts.args) == 0 {
		return a.usageError("service requires a subcommand (list|show|clone)")
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
			return a.usageError("service show requires a service name")
		}
		return a.showResolvedService(opts, cfg, opts.args[1])
	case "clone":
		if len(opts.args) < 3 {
			return a.usageError("service clone requires SOURCE and TARGET")
		}
		return a.cloneService(opts, cfg, opts.args[1], opts.args[2])
	default:
		return a.usageError(fmt.Sprintf("unknown service subcommand %q", opts.args[0]))
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
		return a.fail(opts, fmt.Sprintf("unknown source service %q", source))
	}
	if _, ok := cfg.Services[target]; ok {
		return a.fail(opts, fmt.Sprintf("target service %q already exists", target))
	}
	if len(cfg.Global.Includes) == 0 {
		return a.fail(opts, "no include directory configured (paths.includes)")
	}

	dir := cfg.Global.Includes[0]
	path := filepath.Join(dir, target+".yml")
	content := fmt.Sprintf("kind: service\nname: %s\nclone: %s\n", target, source)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // G306: generated service YAML is non-sensitive (0644)
		return a.fail(opts, fmt.Sprintf("write %s: %v", path, err))
	}
	fmt.Fprintf(a.Stdout, "created %s\n", path)
	return exitSuccess
}

// requireService reports an unknown-service error unless name is configured.
// It returns exitSuccess when the service exists.
func (a App) requireService(opts options, cfg *config.Config, name string) int {
	if _, ok := cfg.Services[name]; !ok {
		return a.fail(opts, fmt.Sprintf("unknown service %q", name))
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
		return a.fail(opts, fmt.Sprintf("render %s: %v", r.Name, err))
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
			out = append(out, map[string]string{"name": n, "display_name": displayNameOf(docs, n)})
		}
		writeJSON(a.Stdout, map[string]any{kind: out})
		return
	}
	if len(names) == 0 {
		fmt.Fprintf(a.Stdout, "no %s\n", kind)
		return
	}
	for _, n := range names {
		display := displayNameOf(docs, n)
		if display == n {
			fmt.Fprintln(a.Stdout, n)
		} else {
			fmt.Fprintf(a.Stdout, "%s\t%s\n", n, display)
		}
	}
}

// displayNameOf returns the document's display_name, falling back to the id
// when the document is absent.
func displayNameOf(docs map[string]*config.Document, n string) string {
	if doc, ok := docs[n]; ok {
		return config.DisplayName(doc.Body, n)
	}
	return n
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
