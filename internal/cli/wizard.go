package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/goccy/go-yaml"

	"sermo/internal/assist"
	"sermo/internal/config"
	"sermo/internal/servicemgr"
	"sermo/internal/volume"
)

// runWizard drives the interactive assistant that generates `watches:` config.
// `sermoctl wizard [name]` runs the named assistant, or lists them to choose.
func (a App) runWizard(ctx context.Context, opts options) int {
	code, err := a.runWizardSession(ctx, opts)
	if err != nil {
		// Piped/truncated stdin: a prompt was still waiting when input ended.
		a.reportError(opts, "wizard aborted: "+err.Error())
		return exitUsage
	}
	return code
}

// runWizardSession is runWizard's prompt-driving body. assist.Recover turns an
// input EOF mid-prompt into ErrInputClosed so a truncated pipe aborts cleanly
// instead of re-prompting forever.
func (a App) runWizardSession(ctx context.Context, opts options) (code int, err error) {
	defer assist.Recover(&err)
	globalPath := opts.globalPath()
	cfg, err := a.LoadConfig(globalPath)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("load config failed: %v", err))
		return exitRuntimeError, nil
	}

	p := assist.NewPrompt(a.wizardStdin(), a.Stdout)

	as, code := a.selectAssistant(p, opts)
	if as == nil {
		return code, nil
	}

	env := a.wizardEnv(ctx, opts, cfg)
	res, err := as.Run(p, env)
	if errors.Is(err, assist.ErrInputClosed) {
		// The assistants recover the mid-prompt EOF themselves; bubble it up to
		// runWizard's "wizard aborted" usage exit, same as an EOF outside Run.
		return 0, err
	}
	if err != nil {
		a.reportError(opts, err.Error())
		return exitRuntimeError, nil
	}
	if len(res.Services) > 0 {
		return a.writeWizardServices(p, opts, globalPath, cfg, res, env), nil
	}
	if len(res.Watches) == 0 {
		fmt.Fprintln(a.Stdout, "Nothing selected; no configuration generated.")
		return exitSuccess, nil
	}

	data, err := yaml.Marshal(map[string]any{"watches": res.Watches})
	if err != nil {
		a.reportError(opts, fmt.Sprintf("render config: %v", err))
		return exitRuntimeError, nil
	}
	fmt.Fprintf(a.Stdout, "\nGenerated configuration (%s):\n\n%s\n", res.Summary, data)

	if !p.Confirm("Merge this into "+globalPath+"?", false) {
		fmt.Fprintln(a.Stdout, "Not written — paste the block above into a YAML file loaded from paths.includes.")
		return exitSuccess, nil
	}
	var deletes []string
	detected := detectedTargetKeys(env, as.Name())
	for _, dir := range wizardCleanupDirs(globalPath, as.Name(), res.Watches) {
		more, err := planWizardWatchDeletes(p, dir, detected)
		if err != nil {
			a.reportError(opts, err.Error())
			return exitRuntimeError, nil
		}
		deletes = append(deletes, more...)
	}
	if err := deleteWizardWatchFiles(deletes); err != nil {
		a.reportError(opts, err.Error())
		return exitRuntimeError, nil
	}
	if len(deletes) > 0 {
		cfg, err = a.LoadConfig(globalPath)
		if err != nil {
			a.reportError(opts, fmt.Sprintf("reload config after deleting old watches failed: %v", err))
			return exitRuntimeError, nil
		}
	}
	if err := ensureNoWatchCollisions(cfg, res.Watches); err != nil {
		a.reportError(opts, err.Error())
		return exitRuntimeError, nil
	}
	merged, err := mergeWizardWatches(globalPath, as.Name(), res.Watches)
	if err != nil {
		a.reportError(opts, err.Error())
		return exitRuntimeError, nil
	}
	if merged.Backup != "" {
		fmt.Fprintf(a.Stdout, "Updated %s paths.includes (backup: %s).\n", globalPath, merged.Backup)
	}
	if len(deletes) > 0 {
		fmt.Fprintf(a.Stdout, "Deleted %d existing watch file(s).\n", len(deletes))
	}
	fmt.Fprintf(a.Stdout, "Wrote %d watch file(s) under %s. Run `sermoctl reload` to apply.\n", len(merged.Files), merged.Dir)
	return exitSuccess, nil
}

// selectAssistant resolves the assistant from the first positional argument, or
// asks the user to pick one. It returns a nil assistant with an exit code when
// it cannot proceed.
func (a App) selectAssistant(p *assist.Prompt, opts options) (assist.Assistant, int) {
	if name := opts.service(); name != "" {
		as, ok := assist.Lookup(name)
		if !ok {
			a.reportError(opts, fmt.Sprintf("unknown assistant %q", name))
			return nil, exitUsage
		}
		return as, exitSuccess
	}
	all := assist.Assistants()
	labels := make([]string, len(all))
	for i, as := range all {
		labels[i] = as.Title()
	}
	return all[p.Choose("Which kind of check do you want to add?", labels)], exitSuccess
}

func (a App) wizardStdin() io.Reader {
	if a.Stdin != nil {
		return a.Stdin
	}
	return os.Stdin
}

// wizardEnv builds the host facts an assistant needs. The wizardEnvFunc seam
// lets tests supply controlled volumes/interfaces.
func (a App) wizardEnv(ctx context.Context, opts options, cfg *config.Config) assist.Env {
	if a.wizardEnvFunc != nil {
		return a.wizardEnvFunc(cfg)
	}
	backend := ""
	if det, err := a.Detector.Detect(ctx, opts.backend); err == nil {
		backend = string(det.Backend)
	}
	return assist.Env{
		Notifiers:     notifierNames(cfg),
		DefaultNotify: config.NotifyDefault(cfg.Global.Raw),
		Backend:       backend,
		Volumes:       listVolumes,
		Ifaces:        listIfaces,
		ServiceNames:  serviceNameSet(cfg),
		Daemons: func() ([]assist.DaemonCandidate, error) {
			if backend == "" {
				return nil, nil
			}
			return listInstalledDaemons(ctx, cfg, servicemgr.Backend(backend))
		},
	}
}

// notifierNames returns the configured notifier names, sorted.
func notifierNames(cfg *config.Config) []string {
	return slices.Sorted(maps.Keys(cfg.Notifiers()))
}

func listVolumes() ([]assist.Volume, error) {
	mounts, err := volume.List(nil)
	if err != nil {
		return nil, err
	}
	out := make([]assist.Volume, len(mounts))
	for i, m := range mounts {
		out[i] = assist.Volume{Mountpoint: m.Mountpoint, FSType: m.FSType, Device: m.Device}
	}
	return out, nil
}

func listIfaces() ([]assist.Iface, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]assist.Iface, 0, len(ifs))
	for _, in := range ifs {
		out = append(out, assist.Iface{
			Name:     in.Name,
			Up:       in.Flags&net.FlagUp != 0 && in.Flags&net.FlagRunning != 0,
			Loopback: in.Flags&net.FlagLoopback != 0,
		})
	}
	return out, nil
}

type wizardMergeResult struct {
	Backup string
	Dir    string
	Files  []string
}

func ensureNoWatchCollisions(cfg *config.Config, fragment map[string]any) error {
	if cfg == nil {
		return nil
	}
	watches, _ := cfg.Global.Raw["watches"].(map[string]any)
	for name := range fragment {
		if _, exists := watches[name]; exists {
			return fmt.Errorf("watch %q already exists in loaded config; not overwriting", name)
		}
	}
	return nil
}

// mergeWizardWatches writes one generated watch per YAML file under a directory
// named after the generated watch type, then ensures that directory is listed in
// paths.includes. Included watch fragments contain a top-level watches map, so the
// loader can merge them into global watch configuration without rewriting
// sermo.yml on every generated watch.
func mergeWizardWatches(path, wizard string, fragment map[string]any) (wizardMergeResult, error) {
	relDir, targetDir := wizardTargetDir(path, wizard, fragment)

	var files []string
	for _, name := range slices.Sorted(maps.Keys(fragment)) {
		file := filepath.Join(targetDir, watchConfigFileName(name))
		if _, err := os.Stat(file); err == nil {
			return wizardMergeResult{}, fmt.Errorf("watch file %s already exists; not overwriting", file)
		} else if !os.IsNotExist(err) {
			return wizardMergeResult{}, fmt.Errorf("stat %s: %w", file, err)
		}
		files = append(files, file)
	}

	bak, err := ensureIncludeDir(path, relDir, targetDir)
	if err != nil {
		return wizardMergeResult{}, err
	}

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return wizardMergeResult{}, fmt.Errorf("create %s: %w", targetDir, err)
	}
	for _, name := range slices.Sorted(maps.Keys(fragment)) {
		file := filepath.Join(targetDir, watchConfigFileName(name))
		data, err := yaml.Marshal(map[string]any{"watches": map[string]any{name: fragment[name]}})
		if err != nil {
			return wizardMergeResult{}, fmt.Errorf("render %s: %w", file, err)
		}
		if err := os.WriteFile(file, data, 0o644); err != nil { //nolint:gosec // config is world-readable by design
			return wizardMergeResult{}, fmt.Errorf("write %s: %w", file, err)
		}
	}
	return wizardMergeResult{Backup: bak, Dir: targetDir, Files: files}, nil
}

func wizardTargetDir(path, wizard string, fragment map[string]any) (string, string) {
	dirName := wizardConfigDirName(wizard, fragment)
	base := filepath.Dir(filepath.Clean(path))
	return dirName, filepath.Join(base, dirName)
}

func wizardCleanupDirs(path, wizard string, fragment map[string]any) []string {
	_, targetDir := wizardTargetDir(path, wizard, fragment)
	dirs := []string{targetDir}
	legacyName := safeConfigPathName(wizard)
	if legacyName == "" {
		return dirs
	}
	legacyDir := filepath.Join(filepath.Dir(filepath.Clean(path)), legacyName)
	if filepath.Clean(legacyDir) != filepath.Clean(targetDir) {
		dirs = append(dirs, legacyDir)
	}
	return dirs
}

func wizardConfigDirName(wizard string, fragment map[string]any) string {
	dirName := ""
	for _, name := range slices.Sorted(maps.Keys(fragment)) {
		checkType := watchFragmentCheckType(fragment[name])
		if checkType == "" {
			continue
		}
		next := watchTypeDirName(checkType)
		if dirName == "" {
			dirName = next
			continue
		}
		if dirName != next {
			dirName = ""
			break
		}
	}
	if dirName == "" {
		dirName = safeConfigPathName(wizard)
	}
	if dirName == "" {
		dirName = "wizard"
	}
	return dirName
}

func watchFragmentCheckType(v any) string {
	entry, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	check, ok := entry["check"].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := check["type"].(string)
	return s
}

func watchTypeDirName(checkType string) string {
	switch strings.ToLower(checkType) {
	case "storage", "disk", "mount":
		return "storage"
	case "net", "network", "icmp":
		return "network"
	default:
		return safeConfigPathName(checkType)
	}
}

type wizardWatchFile struct {
	Path    string
	Names   []string // watch names declared in the file
	Targets []string // host targets monitored (storage paths, interface names)
}

// planWizardWatchDeletes offers to delete managed watch files whose target is no
// longer present on the host — the step-9 cleanup of docs/wizards.md ("delete
// the files whose target we no longer detect"). detected is the set of currently
// detected target keys (mountpoints / interface names); a file is offered only
// when every target it monitors is absent from that set. When detection is
// empty (unavailable, or an assistant without host targets) nothing is offered,
// so a valid file is never proposed for deletion.
func planWizardWatchDeletes(p *assist.Prompt, targetDir string, detected map[string]bool) ([]string, error) {
	files, err := existingWizardWatchFiles(targetDir)
	if err != nil {
		return nil, err
	}
	var stale []wizardWatchFile
	for _, f := range files {
		if targetsStale(f.Targets, detected) {
			stale = append(stale, f)
		}
	}
	if len(stale) == 0 {
		return nil, nil
	}
	if !p.Confirm(fmt.Sprintf("Found %d managed watch file(s) in %s whose target is no longer detected. Review them for deletion?", len(stale), targetDir), true) {
		return nil, nil
	}
	var deletes []string
	for _, f := range stale {
		label := f.Path
		if len(f.Names) > 0 {
			label += " (" + strings.Join(f.Names, ", ") + ")"
		}
		if p.Confirm("Delete stale watch file "+label+"?", true) {
			deletes = append(deletes, f.Path)
		}
	}
	return deletes, nil
}

// targetsStale reports whether every target in the slice is absent from the
// detected set. An empty target list, or an empty detected set (detection
// unavailable, or an assistant without host targets), is never stale — the
// wizard must not propose deleting a file it cannot prove is orphaned.
func targetsStale(targets []string, detected map[string]bool) bool {
	if len(detected) == 0 || len(targets) == 0 {
		return false
	}
	for _, t := range targets {
		if detected[t] {
			return false
		}
	}
	return true
}

// detectedTargetKeys returns the set of host targets the wizard currently
// detects for an assistant, so the cleanup step can tell which managed files are
// orphaned. Keys mirror parseWatchFile's targets (mountpoints for volume,
// interface names for net/uplink) and service daemon names for the service
// wizard. It
// reads from the same Env the assistant used, so tests control it.
func detectedTargetKeys(env assist.Env, wizard string) map[string]bool {
	keys := map[string]bool{}
	switch wizard {
	case "volume":
		if env.Volumes != nil {
			if vols, err := env.Volumes(); err == nil {
				for _, v := range vols {
					keys[v.Mountpoint] = true
				}
			}
		}
	case "net", "uplink":
		if env.Ifaces != nil {
			if ifs, err := env.Ifaces(); err == nil {
				for _, i := range ifs {
					keys[i.Name] = true
				}
			}
		}
	case "service":
		if env.Daemons != nil {
			if ds, err := env.Daemons(); err == nil {
				for _, d := range ds {
					keys[d.Name] = true
				}
			}
		}
	}
	return keys
}

func existingWizardWatchFiles(targetDir string) ([]wizardWatchFile, error) {
	entries, err := os.ReadDir(targetDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read existing watch directory %s: %w", targetDir, err)
	}
	var files []wizardWatchFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		path := filepath.Join(targetDir, name)
		names, targets := parseWatchFile(path)
		files = append(files, wizardWatchFile{Path: path, Names: names, Targets: targets})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// parseWatchFile reads a managed watch fragment once and returns both the watch
// names it declares and the host targets they monitor (the `check.path` of
// storage watches and the `check.interface` of net/route/icmp/dns watches —
// keys that match detectedTargetKeys). nil/nil on any read or parse error.
func parseWatchFile(path string) (names, targets []string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, nil
	}
	watches, _ := root["watches"].(map[string]any)
	for _, v := range watches {
		entry, ok := v.(map[string]any)
		if !ok {
			continue
		}
		check, ok := entry["check"].(map[string]any)
		if !ok {
			continue
		}
		if s, _ := check["path"].(string); s != "" {
			targets = append(targets, s)
		}
		if s, _ := check["interface"].(string); s != "" {
			targets = append(targets, s)
		}
	}
	if len(watches) > 0 {
		names = slices.Sorted(maps.Keys(watches))
	}
	return names, targets
}

func deleteWizardWatchFiles(files []string) error {
	for _, file := range files {
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete existing watch file %s: %w", file, err)
		}
	}
	return nil
}

func watchConfigFileName(name string) string {
	base := safeConfigPathName(name)
	if base == "" {
		base = "watch"
	}
	return base + ".yml"
}

// ensureIncludeDir makes sure targetDir (whose path relative to the config dir
// is relDir) is listed in paths.includes of the global config, rewriting the
// file — keeping a .bak of the original — only when a change is needed. It
// returns the backup path written, or "" when paths.includes already covered it.
// Shared by the watch and service writers so the read/parse/backup dance lives
// in one place.
func ensureIncludeDir(globalPath, relDir, targetDir string) (string, error) {
	orig, err := os.ReadFile(globalPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", globalPath, err)
	}
	var root map[string]any
	if err := yaml.Unmarshal(orig, &root); err != nil {
		return "", fmt.Errorf("parse %s: %w", globalPath, err)
	}
	if root == nil {
		root = map[string]any{}
	}
	changed, err := ensureIncludesPath(root, filepath.Dir(filepath.Clean(globalPath)), relDir, targetDir)
	if err != nil {
		return "", err
	}
	if !changed {
		return "", nil
	}
	out, err := yaml.Marshal(root)
	if err != nil {
		return "", fmt.Errorf("render %s: %w", globalPath, err)
	}
	bak := globalPath + ".bak"
	if err := os.WriteFile(bak, orig, 0o644); err != nil { //nolint:gosec // config is world-readable by design
		return "", fmt.Errorf("write backup %s: %w", bak, err)
	}
	if err := os.WriteFile(globalPath, out, 0o644); err != nil { //nolint:gosec // config is world-readable by design
		return "", fmt.Errorf("write %s: %w", globalPath, err)
	}
	return bak, nil
}

func ensureIncludesPath(root map[string]any, base, relDir, targetDir string) (bool, error) {
	paths, _ := root["paths"].(map[string]any)
	if paths == nil {
		paths = map[string]any{}
		root["paths"] = paths
	}
	list, err := yamlStringList(paths["includes"])
	if err != nil {
		return false, fmt.Errorf("paths.includes must be a string or list before wizard can append")
	}
	legacy, err := yamlStringList(paths["enabled"])
	if err != nil {
		return false, fmt.Errorf("paths.enabled must be a string or list before wizard can migrate it to includes")
	}
	changed := false
	if len(legacy) > 0 {
		list = appendUniqueStrings(list, legacy...)
		delete(paths, "enabled")
		changed = true
	}
	if len(list) == 0 {
		// Seeding the conventional services dir is itself a change: the loader
		// has no implicit default, so an unwritten seed would leave the files
		// the wizard writes under apps never loaded.
		list = append(list, "apps")
		changed = true
	}
	for _, item := range list {
		if sameConfigPath(base, item, targetDir) {
			if changed {
				paths["includes"] = list
			}
			return changed, nil
		}
	}
	paths["includes"] = append(list, relDir)
	return true, nil
}

func appendUniqueStrings(list []string, values ...string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(list)+len(values))
	for _, item := range append(list, values...) {
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func yamlStringList(v any) ([]string, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case string:
		if x == "" {
			return nil, nil
		}
		return []string{x}, nil
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("non-string item")
			}
			if s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	case []string:
		return append([]string(nil), x...), nil
	default:
		return nil, fmt.Errorf("unsupported")
	}
}

func sameConfigPath(base, item, target string) bool {
	if item == "" {
		return false
	}
	p := item
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p) == filepath.Clean(target)
}

func safeConfigPathName(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(name) {
		ok := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.'
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-.")
}
