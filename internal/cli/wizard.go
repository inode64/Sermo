package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
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
	globalPath := opts.config
	if globalPath == "" {
		globalPath = config.DefaultGlobalPath
	}
	cfg, err := a.LoadConfig(globalPath)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("load config failed: %v", err))
		return exitRuntimeError
	}

	p := assist.NewPrompt(a.wizardStdin(), a.Stdout)

	as, code := a.selectAssistant(p, opts)
	if as == nil {
		return code
	}

	res, err := as.Run(p, a.wizardEnv(ctx, opts, cfg))
	if err != nil {
		a.reportError(opts, err.Error())
		return exitRuntimeError
	}
	if len(res.Services) > 0 {
		return a.writeWizardServices(p, opts, globalPath, cfg, res)
	}
	if len(res.Watches) == 0 {
		fmt.Fprintln(a.Stdout, "Nothing selected; no configuration generated.")
		return exitSuccess
	}

	data, err := yaml.Marshal(map[string]any{"watches": res.Watches})
	if err != nil {
		a.reportError(opts, fmt.Sprintf("render config: %v", err))
		return exitRuntimeError
	}
	fmt.Fprintf(a.Stdout, "\nGenerated configuration (%s):\n\n%s\n", res.Summary, data)

	if !p.Confirm("Merge this into "+globalPath+"?", false) {
		fmt.Fprintln(a.Stdout, "Not written — paste the block above into a YAML file loaded from paths.includes.")
		return exitSuccess
	}
	var deletes []string
	for _, dir := range wizardCleanupDirs(globalPath, as.Name(), res.Watches) {
		more, err := planWizardWatchDeletes(p, dir)
		if err != nil {
			a.reportError(opts, err.Error())
			return exitRuntimeError
		}
		deletes = append(deletes, more...)
	}
	if err := deleteWizardWatchFiles(deletes); err != nil {
		a.reportError(opts, err.Error())
		return exitRuntimeError
	}
	if len(deletes) > 0 {
		cfg, err = a.LoadConfig(globalPath)
		if err != nil {
			a.reportError(opts, fmt.Sprintf("reload config after deleting old watches failed: %v", err))
			return exitRuntimeError
		}
	}
	if err := ensureNoWatchCollisions(cfg, res.Watches); err != nil {
		a.reportError(opts, err.Error())
		return exitRuntimeError
	}
	merged, err := mergeWizardWatches(globalPath, as.Name(), res.Watches)
	if err != nil {
		a.reportError(opts, err.Error())
		return exitRuntimeError
	}
	if merged.Backup != "" {
		fmt.Fprintf(a.Stdout, "Updated %s paths.includes (backup: %s).\n", globalPath, merged.Backup)
	}
	if len(deletes) > 0 {
		fmt.Fprintf(a.Stdout, "Deleted %d existing watch file(s).\n", len(deletes))
	}
	fmt.Fprintf(a.Stdout, "Wrote %d watch file(s) under %s. Run `sermoctl reload` to apply.\n", len(merged.Files), merged.Dir)
	return exitSuccess
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
	raw, _ := cfg.Global.Raw["notifiers"].(map[string]any)
	names := make([]string, 0, len(raw))
	for n := range raw {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
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
	orig, err := os.ReadFile(path)
	if err != nil {
		return wizardMergeResult{}, fmt.Errorf("read %s: %w", path, err)
	}
	var root map[string]any
	if err := yaml.Unmarshal(orig, &root); err != nil {
		return wizardMergeResult{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if root == nil {
		root = map[string]any{}
	}

	base := filepath.Dir(filepath.Clean(path))
	relDir, targetDir := wizardTargetDir(path, wizard, fragment)

	var files []string
	for _, name := range sortedMapKeys(fragment) {
		file := filepath.Join(targetDir, watchConfigFileName(name))
		if _, err := os.Stat(file); err == nil {
			return wizardMergeResult{}, fmt.Errorf("watch file %s already exists; not overwriting", file)
		} else if !os.IsNotExist(err) {
			return wizardMergeResult{}, fmt.Errorf("stat %s: %w", file, err)
		}
		files = append(files, file)
	}

	changed, err := ensureIncludesPath(root, base, relDir, targetDir)
	if err != nil {
		return wizardMergeResult{}, err
	}

	var bak string
	if changed {
		out, err := yaml.Marshal(root)
		if err != nil {
			return wizardMergeResult{}, fmt.Errorf("render %s: %w", path, err)
		}
		bak = path + ".bak"
		if err := os.WriteFile(bak, orig, 0o644); err != nil { //nolint:gosec // config is world-readable by design
			return wizardMergeResult{}, fmt.Errorf("write backup %s: %w", bak, err)
		}
		if err := os.WriteFile(path, out, 0o644); err != nil { //nolint:gosec // config is world-readable by design
			return wizardMergeResult{}, fmt.Errorf("write %s: %w", path, err)
		}
	}

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return wizardMergeResult{}, fmt.Errorf("create %s: %w", targetDir, err)
	}
	for _, name := range sortedMapKeys(fragment) {
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
	for _, name := range sortedMapKeys(fragment) {
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
	Path  string
	Names []string
}

func planWizardWatchDeletes(p *assist.Prompt, targetDir string) ([]string, error) {
	files, err := existingWizardWatchFiles(targetDir)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, nil
	}
	if !p.Confirm(fmt.Sprintf("Found %d existing watch file(s) in %s. Review them for deletion?", len(files), targetDir), false) {
		return nil, nil
	}
	var deletes []string
	for _, f := range files {
		label := f.Path
		if len(f.Names) > 0 {
			label += " (" + strings.Join(f.Names, ", ") + ")"
		}
		if p.Confirm("Delete existing watch file "+label+"?", false) {
			deletes = append(deletes, f.Path)
		}
	}
	return deletes, nil
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
		files = append(files, wizardWatchFile{Path: path, Names: watchNamesInFile(path)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func watchNamesInFile(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil
	}
	watches, _ := root["watches"].(map[string]any)
	if len(watches) == 0 {
		return nil
	}
	return sortedMapKeys(watches)
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
		list = append(list, "apps-enabled")
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

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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
