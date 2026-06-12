package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"sermo/internal/assist"
	"sermo/internal/cfgval"
	"sermo/internal/config"
	"sermo/internal/servicemgr"

	"github.com/goccy/go-yaml"
)

// listInstalledDaemons returns the catalog service daemons whose init unit exists
// on the active backend, with each one's resolved unit, default port (and whether
// it is listening) and any declared config files that exist — the facts the
// service wizard shows. Forcing trust=false makes the resolver verify unit
// existence (systemd `systemctl cat`, OpenRC the init script), so a daemon is
// offered only when it is actually installed.
func listInstalledDaemons(ctx context.Context, cfg *config.Config, backend servicemgr.Backend) ([]assist.DaemonCandidate, error) {
	resolver := servicemgr.NewUnitResolver()
	var out []assist.DaemonCandidate
	for _, name := range cfg.DaemonsInCategory(config.CategoryService) {
		resolved, errs := cfg.ResolveCatalog(config.CategoryService, name)
		if len(errs) > 0 || resolved.Tree == nil {
			continue
		}
		candidates, _ := config.ServiceCandidates(resolved.Tree, string(backend), name)
		unit, err := resolver.Resolve(ctx, backend, candidates, false)
		if err != nil {
			continue // not installed on this backend
		}
		c := assist.DaemonCandidate{
			Name:        name,
			Title:       daemonTitle(resolved.Tree, name),
			Unit:        unit,
			UnitPresent: true,
			Port:        daemonPort(resolved.Tree),
			ConfigPaths: existingConfigFiles(resolved.Tree),
		}
		if c.Port > 0 {
			c.PortListening = portListening(c.Port)
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func daemonTitle(tree map[string]any, fallback string) string {
	if s := cfgval.AsString(tree["display_name"]); s != "" {
		return s
	}
	return fallback
}

// daemonPort reads the daemon's default port from its variables (0 if none).
func daemonPort(tree map[string]any) int {
	vars, ok := tree["variables"].(map[string]any)
	if !ok {
		return 0
	}
	if p, ok := cfgval.Int(vars["port"]); ok {
		return p
	}
	return 0
}

// existingConfigFiles returns the daemon's declared `config_files` that exist on
// the host (a catalog hint; empty when not declared).
func existingConfigFiles(tree map[string]any) []string {
	var out []string
	for _, f := range cfgval.StringList(tree["config_files"]) {
		if pathExists(f) {
			out = append(out, f)
		}
	}
	return out
}

// portListening reports whether something accepts TCP on 127.0.0.1:port.
func portListening(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 250*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// servicesIncludeDir is the includes subdirectory the service wizard writes
// kind:service files into (the conventional enabled-services location).
const servicesIncludeDir = "apps-enabled"

// writeWizardServices renders the generated services, confirms, then writes one
// `kind: service` file per service into the apps-enabled includes directory and
// ensures that directory is listed in paths.includes.
func (a App) writeWizardServices(p *assist.Prompt, opts options, globalPath string, cfg *config.Config, res assist.Result) int {
	existing := serviceNameSet(cfg)
	docs := map[string]map[string]any{}
	for name, body := range res.Services {
		if _, dup := existing[name]; dup {
			a.reportError(opts, "service "+name+" is already configured; not overwriting")
			return exitRuntimeError
		}
		doc := map[string]any{"kind": "service", "name": name}
		if b, ok := body.(map[string]any); ok {
			for k, v := range b {
				doc[k] = v
			}
		}
		docs[name] = doc
	}

	preview, err := yaml.Marshal(docsPreview(docs))
	if err != nil {
		a.reportError(opts, fmt.Sprintf("render services: %v", err))
		return exitRuntimeError
	}
	fmt.Fprintf(a.Stdout, "\nGenerated services (%s):\n\n%s\n", res.Summary, preview)
	if !p.Confirm("Write these service files and enable them?", false) {
		fmt.Fprintln(a.Stdout, "Not written — paste the blocks above into files under a paths.includes directory.")
		return exitSuccess
	}

	dir, written, err := writeServiceFiles(globalPath, docs)
	if err != nil {
		a.reportError(opts, err.Error())
		return exitRuntimeError
	}
	fmt.Fprintf(a.Stdout, "Wrote %d service file(s) under %s. Run `sermoctl reload` to apply.\n", written, dir)
	return exitSuccess
}

func docsPreview(docs map[string]map[string]any) []any {
	names := make([]string, 0, len(docs))
	for n := range docs {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]any, 0, len(names))
	for _, n := range names {
		out = append(out, docs[n])
	}
	return out
}

// writeServiceFiles writes each service doc to its own file under the
// apps-enabled includes dir, ensuring that dir is in paths.includes.
func writeServiceFiles(globalPath string, docs map[string]map[string]any) (string, int, error) {
	orig, err := os.ReadFile(globalPath)
	if err != nil {
		return "", 0, fmt.Errorf("read %s: %w", globalPath, err)
	}
	var root map[string]any
	if err := yaml.Unmarshal(orig, &root); err != nil {
		return "", 0, fmt.Errorf("parse %s: %w", globalPath, err)
	}
	if root == nil {
		root = map[string]any{}
	}
	base := filepath.Dir(filepath.Clean(globalPath))
	targetDir := filepath.Join(base, servicesIncludeDir)

	changed, err := ensureIncludesPath(root, base, servicesIncludeDir, targetDir)
	if err != nil {
		return "", 0, err
	}
	if changed {
		out, err := yaml.Marshal(root)
		if err != nil {
			return "", 0, fmt.Errorf("render %s: %w", globalPath, err)
		}
		if err := os.WriteFile(globalPath+".bak", orig, 0o644); err != nil { //nolint:gosec // config is world-readable by design
			return "", 0, fmt.Errorf("write backup: %w", err)
		}
		if err := os.WriteFile(globalPath, out, 0o644); err != nil { //nolint:gosec // config is world-readable by design
			return "", 0, fmt.Errorf("write %s: %w", globalPath, err)
		}
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", 0, fmt.Errorf("create %s: %w", targetDir, err)
	}
	n := 0
	for name, doc := range docs {
		file := filepath.Join(targetDir, watchConfigFileName(name))
		if pathExists(file) {
			return "", 0, fmt.Errorf("service file %s already exists; not overwriting", file)
		}
		data, err := yaml.Marshal(doc)
		if err != nil {
			return "", 0, fmt.Errorf("render %s: %w", file, err)
		}
		if err := os.WriteFile(file, data, 0o644); err != nil { //nolint:gosec // config is world-readable by design
			return "", 0, fmt.Errorf("write %s: %w", file, err)
		}
		n++
	}
	return targetDir, n, nil
}

func serviceNameSet(cfg *config.Config) map[string]struct{} {
	out := make(map[string]struct{}, len(cfg.ServiceNames))
	for _, n := range cfg.ServiceNames {
		out[n] = struct{}{}
	}
	return out
}
