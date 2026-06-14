package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"sermo/internal/assist"
	"sermo/internal/cfgval"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/servicemgr"

	"github.com/goccy/go-yaml"
)

// listInstalledDaemons returns active service targets for the wizard: catalog
// daemons whose init unit exists, plus active backend units not backed by the
// catalog. Catalog candidates keep their resolved unit/status/default port and
// config-file hints; generic candidates write self-contained service checks.
func listInstalledDaemons(ctx context.Context, cfg *config.Config, backend servicemgr.Backend, runner execx.Runner, timeout time.Duration) ([]assist.DaemonCandidate, error) {
	if runner == nil {
		runner = execx.CommandRunner{}
	}
	resolver := servicemgr.NewUnitResolver()
	resolver.Runner = runner
	resolver.Timeout = timeout
	manager, _ := servicemgr.NewManager(backend)
	catalogUnits := map[string]struct{}{}
	var out []assist.DaemonCandidate
	for _, name := range cfg.DaemonsInCategory(config.CategoryService) {
		resolved, errs := cfg.ResolveCatalog(config.CategoryService, name)
		if len(errs) > 0 || resolved.Tree == nil {
			continue
		}
		candidates, _ := config.ServiceCandidates(resolved.Tree, string(backend), name)
		addWizardCatalogUnits(catalogUnits, backend, candidates...)
		unit, status, err := resolveWizardDaemonUnit(ctx, resolver, manager, backend, candidates)
		if err != nil {
			continue // not installed on this backend
		}
		addWizardCatalogUnits(catalogUnits, backend, unit)
		c := assist.DaemonCandidate{
			Name:        name,
			Title:       daemonTitle(resolved.Tree, name),
			Unit:        unit,
			Status:      string(status),
			UnitPresent: true,
			Port:        daemonPort(resolved.Tree),
			ConfigPaths: existingConfigFiles(resolved.Tree),
		}
		// Best-effort PID source for the wizard's pidfile/command_match question.
		proc := servicemgr.DetectProcInfo(ctx, runner, nil, backend, unit)
		c.Pidfile, c.Exe, c.Cmd, c.User = proc.Pidfile, proc.Exe, proc.Cmd, proc.User
		if c.Port > 0 {
			c.PortListening = portListening(c.Port)
		}
		out = append(out, c)
	}
	out = dedupeWizardCatalogCandidates(out, backend)

	if units, err := listActiveBackendUnits(ctx, backend, runner, timeout); err == nil {
		for _, unit := range units {
			if wizardUnitKnown(catalogUnits, backend, unit) {
				continue
			}
			name := wizardServiceNameForUnit(backend, unit)
			if name == "" {
				continue
			}
			c := assist.DaemonCandidate{
				Name:        name,
				Title:       name,
				Unit:        unit,
				Status:      string(servicemgr.StatusActive),
				Generic:     true,
				UnitPresent: true,
			}
			proc := servicemgr.DetectProcInfo(ctx, runner, nil, backend, unit)
			c.Pidfile, c.Exe, c.Cmd, c.User = proc.Pidfile, proc.Exe, proc.Cmd, proc.User
			out = append(out, c)
			addWizardCatalogUnits(catalogUnits, backend, unit)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func dedupeWizardCatalogCandidates(cands []assist.DaemonCandidate, backend servicemgr.Backend) []assist.DaemonCandidate {
	seen := map[string]struct{}{}
	out := cands[:0]
	for _, c := range cands {
		if c.Generic || c.Unit == "" {
			out = append(out, c)
			continue
		}
		if wizardUnitKnown(seen, backend, c.Unit) {
			continue
		}
		addWizardCatalogUnits(seen, backend, c.Unit)
		out = append(out, c)
	}
	return out
}

func addWizardCatalogUnits(keys map[string]struct{}, backend servicemgr.Backend, units ...string) {
	for _, unit := range units {
		unit = strings.TrimSpace(unit)
		if unit == "" {
			continue
		}
		keys[unit] = struct{}{}
		if backend == servicemgr.BackendSystemd {
			if !strings.Contains(unit, ".") {
				keys[unit+".service"] = struct{}{}
			}
			if name := strings.TrimSuffix(unit, ".service"); name != unit {
				keys[name] = struct{}{}
			}
		}
	}
}

func wizardUnitKnown(keys map[string]struct{}, backend servicemgr.Backend, unit string) bool {
	unit = strings.TrimSpace(unit)
	if unit == "" {
		return true
	}
	if _, ok := keys[unit]; ok {
		return true
	}
	if backend == servicemgr.BackendSystemd {
		if strings.HasSuffix(unit, ".service") {
			_, ok := keys[strings.TrimSuffix(unit, ".service")]
			return ok
		}
		_, ok := keys[unit+".service"]
		return ok
	}
	return false
}

func listActiveBackendUnits(ctx context.Context, backend servicemgr.Backend, runner execx.Runner, timeout time.Duration) ([]string, error) {
	if runner == nil {
		runner = execx.CommandRunner{}
	}
	switch backend {
	case servicemgr.BackendSystemd:
		res, err := execx.Run(ctx, runner, timeout, "systemctl", "list-units", "--type=service", "--state=active", "--no-legend", "--no-pager")
		if err != nil && strings.TrimSpace(res.Stdout) == "" {
			return nil, err
		}
		return parseSystemdActiveUnits(res.Stdout), nil
	case servicemgr.BackendOpenRC:
		res, err := execx.Run(ctx, runner, timeout, "rc-status", "--all")
		if err != nil && strings.TrimSpace(res.Stdout) == "" {
			return nil, err
		}
		return parseOpenRCActiveUnits(res.Stdout), nil
	default:
		return nil, fmt.Errorf("no active-unit listing for backend %q", backend)
	}
}

func parseSystemdActiveUnits(stdout string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(stdout))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || fields[0] == "UNIT" {
			continue
		}
		if strings.HasSuffix(fields[0], ".service") {
			out = append(out, fields[0])
		}
	}
	return appendUniqueStrings(nil, out...)
}

func parseOpenRCActiveUnits(stdout string) []string {
	var out []string
	inServiceRunlevel := false
	sc := bufio.NewScanner(strings.NewReader(stdout))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "runlevel:"):
			name := strings.TrimSpace(strings.TrimPrefix(lower, "runlevel:"))
			inServiceRunlevel = openRCWizardRunlevel(name)
			continue
		case strings.HasPrefix(lower, "dynamic runlevel:"):
			name := strings.TrimSpace(strings.TrimPrefix(lower, "dynamic runlevel:"))
			inServiceRunlevel = openRCWizardRunlevel(name)
			continue
		}
		if !inServiceRunlevel || !strings.Contains(lower, "started") {
			continue
		}
		if strings.Contains(lower, "not started") || strings.Contains(lower, "stopped") || strings.Contains(lower, "crashed") {
			continue
		}
		beforeState := line
		if i := strings.Index(beforeState, "["); i >= 0 {
			beforeState = beforeState[:i]
		}
		fields := strings.Fields(beforeState)
		if len(fields) == 0 {
			continue
		}
		out = append(out, fields[0])
	}
	// A service started in more than one matched runlevel appears once per
	// section, and those duplicates are not adjacent in out (other services sit
	// between them), so slices.Compact would not collapse them — dedup by value.
	return appendUniqueStrings(nil, out...)
}

func openRCWizardRunlevel(name string) bool {
	switch name {
	case "default", "needed/wanted", "manual", "hotplugged":
		return true
	default:
		return false
	}
}

func wizardServiceNameForUnit(backend servicemgr.Backend, unit string) string {
	name := strings.TrimSpace(unit)
	if backend == servicemgr.BackendSystemd {
		name = strings.TrimSuffix(name, ".service")
	}
	return name
}

func resolveWizardDaemonUnit(ctx context.Context, resolver servicemgr.UnitResolver, manager servicemgr.Manager, backend servicemgr.Backend, candidates []string) (string, servicemgr.Status, error) {
	var firstUnit string
	firstStatus := servicemgr.StatusUnknown
	for _, candidate := range candidates {
		unit, err := resolver.Resolve(ctx, backend, []string{candidate}, false)
		if err != nil {
			continue
		}
		status := daemonUnitStatus(ctx, manager, unit)
		if firstUnit == "" {
			firstUnit, firstStatus = unit, status
		}
		if status == servicemgr.StatusActive {
			return unit, status, nil
		}
	}
	if firstUnit != "" {
		return firstUnit, firstStatus, nil
	}
	unit, err := resolver.Resolve(ctx, backend, candidates, false)
	if err != nil {
		return "", servicemgr.StatusUnknown, err
	}
	return unit, daemonUnitStatus(ctx, manager, unit), nil
}

func daemonUnitStatus(ctx context.Context, manager servicemgr.Manager, unit string) servicemgr.Status {
	if manager == nil {
		return servicemgr.StatusUnknown
	}
	status, err := manager.Status(ctx, unit)
	if err != nil {
		return servicemgr.StatusUnknown
	}
	return status.Status
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

// portListening reports whether the kernel has a TCP listener or UDP socket on
// port. Reading /proc catches UDP daemons and services bound away from loopback,
// which a TCP dial to 127.0.0.1 cannot see.
func portListening(port int) bool {
	for _, table := range []struct {
		path   string
		states map[string]bool
	}{
		{path: "/proc/net/tcp", states: map[string]bool{"0A": true}},
		{path: "/proc/net/tcp6", states: map[string]bool{"0A": true}},
		{path: "/proc/net/udp", states: map[string]bool{"07": true}},
		{path: "/proc/net/udp6", states: map[string]bool{"07": true}},
	} {
		if procPortListening(table.path, port, table.states) {
			return true
		}
	}
	return false
}

func procPortListening(path string, port int, states map[string]bool) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	ok, _ := parseProcSocketTable(f, port, states)
	return ok
}

func parseProcSocketTable(r io.Reader, port int, states map[string]bool) (bool, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 || fields[0] == "sl" {
			continue
		}
		if !states[strings.ToUpper(fields[3])] {
			continue
		}
		_, portHex, ok := strings.Cut(fields[1], ":")
		if !ok {
			continue
		}
		got, err := strconv.ParseUint(portHex, 16, 16)
		if err != nil {
			continue
		}
		if int(got) == port {
			return true, nil
		}
	}
	return false, sc.Err()
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// servicesIncludeDir is the includes subdirectory the service wizard writes
// kind:service files into.
const servicesIncludeDir = "services"

// legacyServicesIncludeDir is the pre-services include directory. Keep it
// active when an operator already lists it so existing service files are not
// silently orphaned.
const legacyServicesIncludeDir = "apps"

// writeWizardServices renders the generated services, confirms, then writes one
// `kind: service` file per service into the services includes directory and
// ensures that directory is listed in paths.includes.
func (a App) writeWizardServices(p *assist.Prompt, opts options, globalPath string, cfg *config.Config, res assist.Result, env assist.Env) int {
	existing := serviceNameSet(cfg)
	docs := map[string]map[string]any{}
	for name, body := range res.Services {
		if _, dup := existing[name]; dup {
			return a.fail(opts, "service "+name+" is already configured; not overwriting")
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
		return a.fail(opts, fmt.Sprintf("render services: %v", err))
	}
	fmt.Fprintf(a.Stdout, "\nGenerated services (%s):\n\n%s\n", res.Summary, preview)
	if !p.Confirm("Write these service files and enable them?", false) {
		fmt.Fprintln(a.Stdout, "Not written — paste the blocks above into files under a paths.includes directory.")
		return exitSuccess
	}

	// Step-9 cleanup: offer to delete managed service files whose catalog daemon
	// is no longer detected on this host (docs/wizards.md).
	var deletes []string
	for _, dir := range serviceCleanupDirs(globalPath, cfg) {
		more, err := planStaleServiceDeletes(p, dir, detectedTargetKeys(env, "service"))
		if err != nil {
			return a.fail(opts, err.Error())
		}
		deletes = append(deletes, more...)
	}
	if err := deleteWizardWatchFiles(deletes); err != nil {
		return a.fail(opts, err.Error())
	}

	dir, written, err := writeServiceFiles(globalPath, docs)
	if err != nil {
		return a.fail(opts, err.Error())
	}
	if len(deletes) > 0 {
		fmt.Fprintf(a.Stdout, "Deleted %d stale service file(s).\n", len(deletes))
	}
	fmt.Fprintf(a.Stdout, "Wrote %d service file(s) under %s. Run `sermoctl reload` to apply.\n", written, dir)
	return exitSuccess
}

func serviceCleanupDirs(globalPath string, cfg *config.Config) []string {
	base := filepath.Dir(filepath.Clean(globalPath))
	dirs := []string{filepath.Join(base, servicesIncludeDir)}
	if cfg == nil {
		return dirs
	}
	legacy := filepath.Join(base, legacyServicesIncludeDir)
	for _, include := range cfg.Global.Includes {
		if filepath.Clean(include) == filepath.Clean(legacy) {
			dirs = append(dirs, legacy)
			break
		}
	}
	return dirs
}

// planStaleServiceDeletes offers to delete managed `kind: service` files under
// an includes dir whose `uses:` daemon (or name) is no longer in the detected
// set. Mirrors planWizardWatchDeletes for the service wizard; a no-op when
// detection is empty so a valid file is never proposed for deletion.
func planStaleServiceDeletes(p *assist.Prompt, dir string, detected map[string]bool) ([]string, error) {
	if len(detected) == 0 {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read services directory %s: %w", dir, err)
	}
	var stale []staleFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		path := filepath.Join(dir, name)
		target := serviceFileTarget(path)
		if target == "" || detected[target] {
			continue
		}
		stale = append(stale, staleFile{path: path, label: path + " (" + target + ")"})
	}
	return confirmStaleDeletes(p, dir, "service", stale), nil
}

// serviceFileTarget returns the catalog daemon a managed service file targets:
// its `uses:` value, or the doc `name` when self-contained. "" when unreadable.
func serviceFileTarget(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return ""
	}
	if s, _ := doc["uses"].(string); s != "" {
		return s
	}
	s, _ := doc["name"].(string)
	return s
}

func docsPreview(docs map[string]map[string]any) []any {
	out := make([]any, 0, len(docs))
	for _, n := range slices.Sorted(maps.Keys(docs)) {
		out = append(out, docs[n])
	}
	return out
}

// writeServiceFiles writes each service doc to its own file under the services
// includes dir, ensuring that dir is in paths.includes.
func writeServiceFiles(globalPath string, docs map[string]map[string]any) (string, int, error) {
	targetDir := filepath.Join(filepath.Dir(filepath.Clean(globalPath)), servicesIncludeDir)
	if _, err := ensureIncludeDir(globalPath, servicesIncludeDir, targetDir); err != nil {
		return "", 0, err
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
