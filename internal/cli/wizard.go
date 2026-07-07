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
	"strconv"
	"strings"
	"unicode"

	"github.com/goccy/go-yaml"

	"sermo/internal/assist"
	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/mountctl"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
	"sermo/internal/volume"
)

const (
	networksConfigDir = "networks"
	watchesConfigDir  = "watches"

	yamlFileExt     = ".yml"
	yamlLongFileExt = ".yaml"

	wizardAssistantMount   = "mount"
	wizardAssistantNet     = "net"
	wizardAssistantService = "service"
	wizardAssistantUplink  = "uplink"
	wizardAssistantVolume  = "volume"

	serviceFamilyDocker = "docker"
	serviceFamilyVM     = "vm"

	wizardNounMount   = wizardAssistantMount
	wizardNounService = wizardAssistantService
	wizardNounStorage = "storage"
	wizardNounWatch   = "watch"

	wizardFieldCheck     = "check"
	wizardFieldInterface = "interface"
	wizardFieldKind      = "kind"
	wizardFieldName      = "name"
	wizardFieldPath      = "path"
	wizardFieldType      = "type"

	serviceTargetSeparator = ":"
)

// runWizard drives the interactive assistant that generates target config.
// `sermoctl wizard [name]` runs the named assistant, or lists them to choose.
func (a App) runWizard(ctx context.Context, opts options) int {
	if len(opts.args) > 1 {
		return a.commandUsageError(commandWizard, "wizard accepts at most one assistant name")
	}
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
	if len(res.Mounts) > 0 {
		return a.writeWizardMounts(p, opts, globalPath, cfg, res, env), nil
	}
	if len(res.Watches) == 0 {
		fmt.Fprintln(a.Stdout, "Nothing selected; no configuration generated.")
		return exitSuccess, nil
	}

	data, err := renderWizardWatchPreview(as.Name(), res.Watches)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("render config: %v", err))
		return exitRuntimeError, nil
	}
	fmt.Fprintf(a.Stdout, "\nGenerated configuration (%s):\n\n%s\n", res.Summary, data)

	if !p.Confirm("Merge this into "+globalPath+"?", false) {
		if wizardWritesStorageDocs(as.Name()) {
			fmt.Fprintln(a.Stdout, "Not written — paste the blocks above into files under a paths.storages directory.")
		} else {
			fmt.Fprintln(a.Stdout, "Not written — paste each block above into its own YAML file loaded from paths.networks or paths.watches.")
		}
		return exitSuccess, nil
	}
	var deletes []string
	detected := detectedTargetKeys(env, as.Name())
	noun := wizardOutputNoun(as.Name())
	for _, dir := range wizardCleanupDirs(globalPath, as.Name(), res.Watches) {
		more, err := planWizardWatchDeletes(p, dir, detected, noun)
		if err != nil {
			a.reportError(opts, err.Error())
			return exitRuntimeError, nil
		}
		deletes = append(deletes, more...)
	}
	if err := deleteWizardConfigFiles(deletes); err != nil {
		a.reportError(opts, err.Error())
		return exitRuntimeError, nil
	}
	if len(deletes) > 0 {
		cfg, err = a.LoadConfig(globalPath)
		if err != nil {
			a.reportError(opts, fmt.Sprintf("reload config after deleting old %s files failed: %v", noun, err))
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
		fmt.Fprintf(a.Stdout, "Updated %s paths.%s (backup: %s).\n", globalPath, merged.PathKey, merged.Backup)
	}
	if len(deletes) > 0 {
		fmt.Fprintf(a.Stdout, "Deleted %d existing %s file(s).\n", len(deletes), noun)
	}
	fmt.Fprintf(a.Stdout, "Wrote %d %s file(s) under %s. Run `sermoctl daemon reload` to apply.\n", len(merged.Files), noun, merged.Dir)
	return exitSuccess, nil
}

func renderWizardWatchPreview(wizard string, entries map[string]any) ([]byte, error) {
	if wizardWritesStorageDocs(wizard) {
		docs, err := storageDocsFromVolumeWatches(entries)
		if err != nil {
			return nil, err
		}
		return yaml.Marshal(docsPreview(docs))
	}
	docs, err := watchDocsFromEntries(entries)
	if err != nil {
		return nil, err
	}
	return yaml.Marshal(docsPreview(docs))
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
		Mounts:        listWizardMounts,
		Ifaces:        listIfaces,
		DefaultIfaces: defaultRouteIfaces(),
		ServiceNames:  serviceNameSet(cfg),
		CatalogServices: func() ([]assist.ServiceCandidate, error) {
			if backend == "" {
				return nil, nil
			}
			return listInstalledCatalogServices(ctx, cfg, servicemgr.Backend(backend), a.Runner, opts.timeout)
		},
		DockerContainers: func() ([]assist.DockerCandidate, error) {
			return listWizardDockerContainers(ctx, opts.timeout)
		},
		VMs: func() ([]assist.VMCandidate, error) {
			return listWizardVMs(ctx, opts.timeout)
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

func listWizardMounts() ([]assist.MountCandidate, error) {
	entries, err := mountctl.FstabEntries("/etc/fstab")
	if err != nil {
		return nil, err
	}
	mounted := map[string]bool{}
	if mounts, err := checks.DefaultMounts(); err == nil {
		for _, m := range mounts {
			mounted[filepath.Clean(m.MountPoint)] = true
		}
	}
	seen := map[string]struct{}{}
	out := make([]assist.MountCandidate, 0, len(entries))
	for _, entry := range entries {
		path := filepath.Clean(entry.Path)
		if !filepath.IsAbs(path) {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, assist.MountCandidate{
			Path:    path,
			Source:  entry.Source,
			FSType:  entry.FSType,
			Options: entry.Options,
			Mounted: mounted[path],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func listIfaces() ([]assist.Iface, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return listIfacesFromSysfs("/sys/class/net")
	}
	out := make([]assist.Iface, 0, len(ifs))
	for _, in := range ifs {
		addrs, _ := in.Addrs()
		out = append(out, assist.Iface{
			Name:       in.Name,
			Up:         in.Flags&net.FlagUp != 0 && in.Flags&net.FlagRunning != 0,
			Loopback:   in.Flags&net.FlagLoopback != 0,
			HasAddress: ifaceHasUsableAddress(addrs),
		})
	}
	if !hasNonLoopbackIface(out) {
		if sysfs, err := listIfacesFromSysfs("/sys/class/net"); err == nil && hasNonLoopbackIface(sysfs) {
			return sysfs, nil
		}
	}
	return out, nil
}

func hasNonLoopbackIface(ifaces []assist.Iface) bool {
	for _, iface := range ifaces {
		if !iface.Loopback {
			return true
		}
	}
	return false
}

func listIfacesFromSysfs(root string) ([]assist.Iface, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := make([]assist.Iface, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		name := entry.Name()
		dir := filepath.Join(root, name)
		flags := sysfsIfaceFlags(filepath.Join(dir, "flags"))
		operstate := strings.TrimSpace(readSmallFile(filepath.Join(dir, "operstate")))
		loopback := flags&0x8 != 0 || name == "lo"
		up := flags&0x1 != 0 && (flags&0x40 != 0 || operstate == "up" || operstate == "unknown")
		out = append(out, assist.Iface{Name: name, Up: up, Loopback: loopback})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func sysfsIfaceFlags(path string) uint64 {
	raw := strings.TrimSpace(readSmallFile(path))
	raw = strings.TrimPrefix(raw, "0x")
	flags, _ := strconv.ParseUint(raw, 16, 64)
	return flags
}

func readSmallFile(path string) string {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return ""
	}
	return string(data)
}

func defaultRouteIfaces() []string {
	seen := map[string]bool{}
	var out []string
	for _, family := range []string{"ipv4", "ipv6"} {
		routes, err := checks.SampleRoutes(family)
		if err != nil {
			continue
		}
		for _, route := range routes {
			if route.Iface == "" || seen[route.Iface] {
				continue
			}
			seen[route.Iface] = true
			out = append(out, route.Iface)
		}
	}
	sort.Strings(out)
	return out
}

func ifaceHasUsableAddress(addrs []net.Addr) bool {
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		if ip.IsGlobalUnicast() {
			return true
		}
	}
	return false
}

type wizardMergeResult struct {
	Backup  string
	Dir     string
	Files   []string
	PathKey string
}

func ensureNoWatchCollisions(cfg *config.Config, entries map[string]any) error {
	if cfg == nil {
		return nil
	}
	watches, _ := cfg.ResolveWatches()
	for name := range entries {
		if _, exists := cfg.Storages[name]; exists {
			return fmt.Errorf("storage %q already exists in loaded config; not overwriting", name)
		}
		if _, exists := watches[name]; exists {
			return fmt.Errorf("watch %q already exists in loaded config; not overwriting", name)
		}
	}
	return nil
}

// mergeWizardWatches writes one generated target per YAML file. Volume output
// is converted to storage documents under paths.storages; other watch
// assistants write watch documents under paths.networks or paths.watches so the
// loader can merge them without rewriting sermo.yml on every generated watch.
func mergeWizardWatches(path, wizard string, entries map[string]any) (wizardMergeResult, error) {
	if wizardWritesStorageDocs(wizard) {
		docs, err := storageDocsFromVolumeWatches(entries)
		if err != nil {
			return wizardMergeResult{}, err
		}
		return mergeWizardStorageDocs(path, docs)
	}
	relDir, targetDir := wizardTargetDir(path, wizard, entries)
	pathKey := wizardPathKey(wizard, entries)
	docs, err := watchDocsFromEntries(entries)
	if err != nil {
		return wizardMergeResult{}, err
	}
	files, bak, err := writeConfigDocs(path, pathKey, relDir, targetDir, wizardNounWatch, docs)
	if err != nil {
		return wizardMergeResult{}, err
	}
	return wizardMergeResult{Backup: bak, Dir: targetDir, Files: plannedConfigFilePaths(files), PathKey: pathKey}, nil
}

func watchDocsFromEntries(entries map[string]any) (map[string]map[string]any, error) {
	out := make(map[string]map[string]any, len(entries))
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		doc, err := watchDocFromEntry(name, entries[name])
		if err != nil {
			return nil, err
		}
		out[name] = doc
	}
	return out, nil
}

func watchDocFromEntry(name string, raw any) (map[string]any, error) {
	entry, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("watch %q is not a mapping", name)
	}
	doc := maps.Clone(entry)
	doc[wizardFieldName] = name
	return doc, nil
}

func wizardWritesStorageDocs(wizard string) bool {
	return wizard == wizardAssistantVolume
}

func wizardOutputNoun(wizard string) string {
	if wizardWritesStorageDocs(wizard) {
		return wizardNounStorage
	}
	return wizardNounWatch
}

func storageDocsFromVolumeWatches(entries map[string]any) (map[string]map[string]any, error) {
	docs := make(map[string]map[string]any, len(entries))
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		entry, ok := entries[name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("watch %q is not a mapping", name)
		}
		check, ok := entry[wizardFieldCheck].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("watch %q has no check mapping", name)
		}
		path, _ := check[wizardFieldPath].(string)
		if path == "" {
			return nil, fmt.Errorf("watch %q has no storage path", name)
		}
		doc := map[string]any{
			wizardFieldName:         name,
			config.EntryKeyCategory: wizardNounStorage,
			wizardFieldPath:         path,
		}
		for _, key := range []string{config.EntryKeyMonitor, config.EntryKeyInterval} {
			if v, present := entry[key]; present {
				doc[key] = v
			}
		}
		capacity := map[string]any{}
		for key, value := range check {
			switch key {
			case wizardFieldType, wizardFieldPath:
			default:
				capacity[key] = value
			}
		}
		for _, key := range []string{rules.RuleFieldFor, rules.RuleFieldWithin, config.WatchKeyThen, rules.SectionPolicy} {
			if v, present := entry[key]; present {
				capacity[key] = v
			}
		}
		doc[config.StorageKeyCapacity] = capacity
		docs[name] = doc
	}
	return docs, nil
}

func mergeWizardStorageDocs(path string, docs map[string]map[string]any) (wizardMergeResult, error) {
	relDir := storagesConfigDir
	targetDir := filepath.Join(filepath.Dir(filepath.Clean(path)), relDir)
	pathKey := storagesConfigDir
	files, bak, err := writeConfigDocs(path, pathKey, relDir, targetDir, wizardNounStorage, docs)
	if err != nil {
		return wizardMergeResult{}, err
	}
	return wizardMergeResult{Backup: bak, Dir: targetDir, Files: plannedConfigFilePaths(files), PathKey: pathKey}, nil
}

func wizardTargetDir(path, wizard string, entries map[string]any) (string, string) {
	dirName := wizardConfigDirName(wizard, entries)
	base := filepath.Dir(filepath.Clean(path))
	return dirName, filepath.Join(base, dirName)
}

func wizardPathKey(wizard string, entries map[string]any) string {
	dirName := wizardConfigDirName(wizard, entries)
	switch dirName {
	case storagesConfigDir:
		return storagesConfigDir
	case networksConfigDir:
		return networksConfigDir
	default:
		return watchesConfigDir
	}
}

func wizardCleanupDirs(path, wizard string, entries map[string]any) []string {
	_, targetDir := wizardTargetDir(path, wizard, entries)
	return []string{targetDir}
}

func wizardConfigDirName(wizard string, entries map[string]any) string {
	if wizardWritesStorageDocs(wizard) {
		return storagesConfigDir
	}
	dirName := ""
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		checkType := watchEntryCheckType(entries[name])
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
		dirName = watchesConfigDir
	}
	return dirName
}

func watchEntryCheckType(v any) string {
	entry, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	check, ok := entry[wizardFieldCheck].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := check[wizardFieldType].(string)
	return s
}

func watchTypeDirName(checkType string) string {
	switch strings.ToLower(checkType) {
	case wizardAssistantNet, "network", "icmp":
		return networksConfigDir
	default:
		return watchesConfigDir
	}
}

type wizardWatchFile struct {
	Path    string
	Names   []string // watch names declared in the file
	Targets []string // host targets monitored (storage paths, interface names)
}

// planWizardWatchDeletes offers to delete managed wizard output files whose
// target is no longer present on the host — the step-9 cleanup of
// docs/wizards.md ("delete the files whose target we no longer detect").
// detected is the set of currently detected target keys (mountpoints /
// interface names); a file is offered only when every target it monitors is
// absent from that set. When detection is empty (unavailable, or an assistant
// without host targets) nothing is offered, so a valid file is never proposed
// for deletion.
func planWizardWatchDeletes(p *assist.Prompt, targetDir string, detected map[string]bool, noun string) ([]string, error) {
	files, err := existingWizardWatchFiles(targetDir)
	if err != nil {
		return nil, err
	}
	var stale []staleFile
	for _, f := range files {
		if !targetsStale(f.Targets, detected) {
			continue
		}
		label := f.Path
		if len(f.Names) > 0 {
			label += " (" + strings.Join(f.Names, ", ") + ")"
		}
		stale = append(stale, staleFile{path: f.Path, label: label})
	}
	return confirmStaleDeletes(p, targetDir, noun, stale), nil
}

// staleFile is a managed config file whose target is no longer detected on the
// host, offered for deletion by the step-9 cleanup. label is the path plus a
// human hint (the watch names, or the catalog service a service uses).
type staleFile struct {
	path  string
	label string
}

// confirmStaleDeletes asks whether to review the stale files, then confirms each
// one, returning the paths the operator chose to delete. noun is the file kind
// ("watch" / "service") used in the prompts. With no stale files it asks nothing
// and returns nil. Shared by the watch and service cleanup planners.
func confirmStaleDeletes(p *assist.Prompt, dir, noun string, stale []staleFile) []string {
	if len(stale) == 0 {
		return nil
	}
	if !p.Confirm(fmt.Sprintf("Found %d managed %s file(s) in %s whose target is no longer detected. Review them for deletion?", len(stale), noun, dir), true) {
		return nil
	}
	var deletes []string
	for _, f := range stale {
		if p.Confirm("Delete stale "+noun+" file "+f.label+"?", true) {
			deletes = append(deletes, f.path)
		}
	}
	return deletes
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
// interface names for net/uplink) and service target names for the service
// wizard. It reads from the same Env the assistant used, so tests control it.
func detectedTargetKeys(env assist.Env, wizard string) map[string]bool {
	keys := map[string]bool{}
	switch wizard {
	case wizardAssistantVolume:
		if env.Volumes != nil {
			if vols, err := env.Volumes(); err == nil {
				for _, v := range vols {
					keys[v.Mountpoint] = true
				}
			}
		}
	case wizardAssistantMount:
		if env.Mounts != nil {
			if mounts, err := env.Mounts(); err == nil {
				for _, m := range mounts {
					keys[filepath.Clean(m.Path)] = true
				}
			}
		}
	case wizardAssistantNet, wizardAssistantUplink:
		if env.Ifaces != nil {
			if ifs, err := env.Ifaces(); err == nil {
				for _, i := range ifs {
					keys[i.Name] = true
				}
			}
		}
	case wizardAssistantService:
		if env.CatalogServices != nil {
			if ds, err := env.CatalogServices(); err == nil {
				if len(ds) > 0 {
					keys[serviceDetectedFamilyKey(wizardNounService)] = true
					for _, d := range ds {
						keys[serviceTargetKey(wizardNounService, d.Name)] = true
					}
				}
			}
		}
		if env.DockerContainers != nil {
			if containers, err := env.DockerContainers(); err == nil {
				if len(containers) > 0 {
					keys[serviceDetectedFamilyKey(serviceFamilyDocker)] = true
					for _, c := range containers {
						keys[serviceTargetKey(serviceFamilyDocker, c.Container)] = true
					}
				}
			}
		}
		if env.VMs != nil {
			if vms, err := env.VMs(); err == nil {
				if len(vms) > 0 {
					keys[serviceDetectedFamilyKey(serviceFamilyVM)] = true
					for _, vm := range vms {
						keys[serviceTargetKey(serviceFamilyVM, vm.Domain)] = true
					}
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
		if !strings.HasSuffix(name, yamlFileExt) && !strings.HasSuffix(name, yamlLongFileExt) {
			continue
		}
		path := filepath.Join(targetDir, name)
		names, targets := parseWatchFile(path)
		files = append(files, wizardWatchFile{Path: path, Names: names, Targets: targets})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// parseWatchFile reads a managed watch document or storage document once and
// returns both the names it declares and the host targets they monitor (the
// storage `path`, the `check.path` of storage watches and the `check.interface`
// of net/route/icmp/dns watches — keys that match detectedTargetKeys). nil/nil
// on any read or parse error.
func parseWatchFile(path string) (names, targets []string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, nil
	}
	if name, _ := root[wizardFieldName].(string); name != "" {
		names = []string{name}
		if path, _ := root[wizardFieldPath].(string); path != "" {
			targets = append(targets, filepath.Clean(path))
			return names, targets
		}
		check, _ := root[wizardFieldCheck].(map[string]any)
		if s, _ := check[wizardFieldPath].(string); s != "" {
			targets = append(targets, s)
		}
		if s, _ := check[wizardFieldInterface].(string); s != "" {
			targets = append(targets, s)
		}
		return names, targets
	}
	return names, targets
}

func deleteWizardConfigFiles(files []string) error {
	for _, file := range files {
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete existing config file %s: %w", file, err)
		}
	}
	return nil
}

func watchConfigFileName(name string) string {
	base := safeConfigPathName(name)
	if base == "" {
		base = wizardNounWatch
	}
	return base + yamlFileExt
}

type plannedConfigFile struct {
	path string
	data []byte
}

func planConfigFiles(targetDir, noun string, docs map[string]map[string]any) ([]plannedConfigFile, error) {
	files := make([]plannedConfigFile, 0, len(docs))
	plannedPaths := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(docs)) {
		file := filepath.Join(targetDir, watchConfigFileName(name))
		if previous, exists := plannedPaths[file]; exists {
			return nil, fmt.Errorf("%s files %q and %q both map to %s; not overwriting", noun, previous, name, file)
		}
		plannedPaths[file] = name
		if _, err := os.Stat(file); err == nil {
			return nil, fmt.Errorf("%s file %s already exists; not overwriting", noun, file)
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat %s: %w", file, err)
		}
		data, err := yaml.Marshal(docs[name])
		if err != nil {
			return nil, fmt.Errorf("render %s: %w", file, err)
		}
		files = append(files, plannedConfigFile{path: file, data: data})
	}
	return files, nil
}

func writeConfigDocs(globalPath, pathKey, relDir, targetDir, noun string, docs map[string]map[string]any) ([]plannedConfigFile, string, error) {
	files, err := planConfigFiles(targetDir, noun, docs)
	if err != nil {
		return nil, "", err
	}
	bak, err := ensureConfigPathDir(globalPath, pathKey, relDir, targetDir)
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, "", fmt.Errorf("create %s: %w", targetDir, err)
	}
	for _, file := range files {
		if err := os.WriteFile(file.path, file.data, 0o644); err != nil { //nolint:gosec // config is world-readable by design
			return nil, "", fmt.Errorf("write %s: %w", file.path, err)
		}
	}
	return files, bak, nil
}

func plannedConfigFilePaths(files []plannedConfigFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.path)
	}
	return paths
}

// ensureConfigPathDir makes sure targetDir (whose path relative to the config
// dir is relDir) is listed in paths.<pathKey> of the global config, rewriting
// the file — keeping a .bak of the original — only when a change is needed. It
// returns the backup path written, or "" when paths.<pathKey> already covered it.
func ensureConfigPathDir(globalPath, pathKey, relDir, targetDir string) (string, error) {
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
	changed, err := ensureConfigPathList(root, filepath.Dir(filepath.Clean(globalPath)), pathKey, relDir, targetDir)
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

func ensureConfigPathList(root map[string]any, base, pathKey, relDir, targetDir string) (bool, error) {
	paths, _ := root[config.SectionPaths].(map[string]any)
	if paths == nil {
		paths = map[string]any{}
		root[config.SectionPaths] = paths
	}
	list, err := cfgval.StrictStringList(paths[pathKey])
	if err != nil {
		return false, fmt.Errorf("paths.%s must be a string or list before wizard can append", pathKey)
	}
	for _, item := range list {
		if sameConfigPath(base, item, targetDir) {
			return false, nil
		}
	}
	paths[pathKey] = append(list, relDir)
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

func sameConfigPath(base, item, target string) bool {
	if item == "" {
		return false
	}
	normalize := func(path string) string {
		if !filepath.IsAbs(path) {
			path = filepath.Join(base, path)
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		return filepath.Clean(path)
	}
	return normalize(item) == normalize(target)
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
