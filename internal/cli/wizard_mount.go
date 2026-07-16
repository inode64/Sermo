package cli

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"

	"sermo/internal/assist"
	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
)

// writeWizardMounts renders generated mount watches under paths.watches.
func (a App) writeWizardMounts(p *assist.Prompt, opts options, globalPath string, cfg *config.Config, res assist.Result, env assist.Env) int {
	docs := map[string]map[string]any{}
	paths := map[string]string{}
	watches, _ := cfg.ResolveWatches()
	for name, body := range res.Mounts {
		if _, dup := watches[name]; dup {
			return a.fail(opts, "watch "+name+" is already configured; not overwriting")
		}
		doc := wizardMountStorageDoc(name, body)
		check, _ := doc[wizardFieldCheck].(map[string]any)
		path := filepath.Clean(cfgval.String(check[wizardFieldPath]))
		if path == "." || path == "" {
			return a.fail(opts, "mount "+name+" has no path; not writing")
		}
		if existing := cfg.StorageNameByPath(path); existing != "" {
			return a.fail(opts, fmt.Sprintf("storage path %s is already configured by watch %s; not overwriting", path, existing))
		}
		if prev := paths[path]; prev != "" {
			return a.fail(opts, fmt.Sprintf("mount path %s selected more than once (%s and %s)", path, prev, name))
		}
		paths[path] = name
		docs[name] = doc
	}

	confirmed, code := a.confirmWizardDocs(
		p, opts, docs, "Generated mount watches", res.Summary, "mounts",
		"Write these mount watch files and enable them?",
		"Not written — paste the blocks above into files under a directory listed in paths.watches.",
	)
	if !confirmed {
		return code
	}

	targetDir := wizardMountTargetDir(globalPath)
	deletes, err := planStaleMountDeletes(p, targetDir, detectedTargetKeys(env, wizardAssistantMount))
	if err != nil {
		return a.fail(opts, err.Error())
	}
	return a.finishWizardWrite(opts, globalPath, "mount watch", deletes, docs, writeMountFiles)
}

// finishWizardWrite runs the tail shared by writeWizardMounts and
// writeWizardServices: delete any stale files, write the generated docs, then
// report both counts. noun labels the files in the user-facing messages
// ("mount watch", "service").
func (a App) finishWizardWrite(opts options, globalPath, noun string, deletes []string, docs map[string]map[string]any, writeFiles func(string, map[string]map[string]any) (string, int, error)) int {
	if err := deleteWizardConfigFiles(deletes); err != nil {
		return a.fail(opts, err.Error())
	}

	dir, written, err := writeFiles(globalPath, docs)
	if err != nil {
		return a.fail(opts, err.Error())
	}
	if len(deletes) > 0 {
		fmt.Fprintf(a.Stdout, "Deleted %d stale %s file(s).\n", len(deletes), noun)
	}
	fmt.Fprintf(a.Stdout, "Wrote %d %s file(s) under %s. Run `sermoctl daemon reload` to apply.\n", written, noun, dir)
	return exitSuccess
}

func wizardMountStorageDoc(name string, body any) map[string]any {
	doc := map[string]any{wizardFieldName: name}
	mount := map[string]any{}
	check := map[string]any{
		checks.CheckKeyType:    checks.CheckTypeStorage,
		checks.CheckKeyMounted: true,
	}
	if b, ok := body.(map[string]any); ok {
		for _, key := range []string{config.EntryKeyDisplayName, config.EntryKeyDescription, config.EntryKeyCategory, config.EntryKeyMonitor, config.EntryKeyInterval} {
			if v, present := b[key]; present {
				doc[key] = v
			}
		}
		if existing, ok := b[config.WatchKeyCheck].(map[string]any); ok {
			maps.Copy(check, existing)
		}
		for _, key := range []string{config.MountKeyRefcount, config.MountKeyUmount, config.MountKeyStopPolicy} {
			if v, present := b[key]; present {
				mount[key] = v
			}
		}
		if existing, ok := b[config.StorageKeyMount].(map[string]any); ok {
			maps.Copy(mount, existing)
		}
	}
	if len(mount) == 0 {
		mount[config.MountKeyRefcount] = true
	}
	doc[wizardFieldCheck] = check
	doc[config.StorageKeyMount] = mount
	return doc
}

func wizardMountTargetDir(globalPath string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(globalPath)), mountsConfigDir)
}

func writeMountFiles(globalPath string, docs map[string]map[string]any) (string, int, error) {
	targetDir := wizardMountTargetDir(globalPath)
	files, _, err := writeConfigDocs(globalPath, watchesConfigDir, mountsConfigDir, targetDir, wizardNounWatch, docs)
	if err != nil {
		return "", 0, err
	}
	return targetDir, len(files), nil
}

func planStaleMountDeletes(p *assist.Prompt, dir string, detected map[string]bool) ([]string, error) {
	return planStaleDeletes(p, dir, wizardNounMount, "mount watches", detected, mountStaleFile)
}

func mountStaleFile(path string, detected map[string]bool) staleFile {
	target := mountFileTarget(path)
	if target == "" || detected[target] {
		return staleFile{}
	}
	return staleFile{path: path, label: path + " (" + target + ")"}
}

func mountFileTarget(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return ""
	}
	if kind, _ := doc[wizardFieldKind].(string); kind != "" && kind != wizardNounWatch {
		return ""
	}
	if _, ok := doc[config.StorageKeyMount].(map[string]any); !ok {
		return ""
	}
	check, _ := doc[wizardFieldCheck].(map[string]any)
	if cfgval.String(check[wizardFieldType]) != checks.CheckTypeStorage {
		return ""
	}
	target := cfgval.String(check[wizardFieldPath])
	if target == "" {
		return ""
	}
	return filepath.Clean(target)
}
